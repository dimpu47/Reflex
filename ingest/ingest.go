package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"jev-proxy/metrics"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// maxPayloadBytes caps the enriched alert state handed to the LLM.
const maxPayloadBytes = 4096

// Alert represents an incoming infrastructure alert.
type Alert struct {
	ID          string          `json:"id"`
	ServiceName string          `json:"service_name"`
	Payload     json.RawMessage `json:"payload"`

	// Resolved is true when the upstream notifier told us this alert has
	// already cleared. Resolved notifications are acknowledged but never
	// remediated.
	Resolved bool `json:"-"`
}

// alertmanagerPayload mirrors the webhook body posted by Prometheus
// Alertmanager. It does not match the native Reflex schema, so alerts
// arriving this way have no top-level id or service_name until normalized.
type alertmanagerPayload struct {
	Status             string `json:"status"`
	GroupKey           string `json:"groupKey"`
	CommonLabels       map[string]string `json:"commonLabels"`
	CommonAnnotations  map[string]string `json:"commonAnnotations"`
	Alerts             []struct {
		Status       string            `json:"status"`
		Labels       map[string]string `json:"labels"`
		Annotations  map[string]string `json:"annotations"`
		StartsAt     string            `json:"startsAt"`
		Fingerprint  string            `json:"fingerprint"`
		GeneratorURL string            `json:"generatorURL"`
	} `json:"alerts"`
}

// normalizeAlert detects an Alertmanager webhook and projects it onto the
// native Alert shape. It reports whether the body was an Alertmanager
// envelope and whether the alert should be dropped as already resolved.
func normalizeAlert(alert *Alert) (fromAlertmanager bool, drop bool) {
	if alert.ID != "" && alert.ServiceName != "" {
		return false, false
	}

	var am alertmanagerPayload
	if err := json.Unmarshal(alert.Payload, &am); err != nil || len(am.Alerts) == 0 {
		return false, false
	}

	first := am.Alerts[0]

	// Fingerprints are stable across repeat notifications for the same
	// alert, which is exactly the identity we want for DAG checkpointing
	// and for Storm-Breaker dedup.
	id := first.Fingerprint
	if id == "" {
		id = am.GroupKey
	}
	if id == "" {
		return false, false
	}
	alert.ID = id

	// Prefer the most specific workload identity available so K8s context
	// enrichment can resolve the right pod.
	labels := first.Labels
	if labels == nil {
		labels = am.CommonLabels
	}
	alert.ServiceName = firstNonEmpty(
		labels["pod"],
		labels["deployment"],
		labels["service"],
		labels["alertname"],
	)

	// Rebuild the payload so the evaluator and the K8s enrichment path see
	// the labels map they already expect.
	rebuilt := map[string]interface{}{
		"alertname":   labels["alertname"],
		"severity":    labels["severity"],
		"namespace":   labels["namespace"],
		"status":      first.Status,
		"summary":     first.Annotations["summary"],
		"description": first.Annotations["description"],
		"startsAt":    first.StartsAt,
		"labels":      labels,
		"annotations": first.Annotations,
		"source":      "alertmanager",
	}
	if enriched, err := json.Marshal(rebuilt); err == nil {
		alert.Payload = enriched
	}

	alert.Resolved = first.Status == "resolved" || am.Status == "resolved"
	return true, alert.Resolved
}

func firstNonEmpty(candidates ...string) string {
	for _, c := range candidates {
		if c != "" {
			return c
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// truncateRunes caps a byte slice at max bytes without splitting a UTF-8
// rune, so downstream json.Unmarshal never sees a partial codepoint.
func truncateRunes(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(b[cut]) {
		cut--
	}
	// Drop any trailing byte sequence that is now an incomplete rune.
	for cut > 0 && !utf8.Valid(b[:cut]) {
		cut--
	}
	return bytes.TrimRight(b[:cut], "\x00")
}

// Handler handles incoming HTTP webhooks and puts them on the channel.
type Handler struct {
	AlertChan chan<- Alert
	Wg        *sync.WaitGroup
	K8sClient *kubernetes.Clientset
}

// ServeHTTP parses the webhook payload and forwards it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var alert Alert
	if err := json.Unmarshal(body, &alert); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	fromAM, resolved := normalizeAlert(&alert)
	if resolved {
		// send_resolved is enabled on the Alertmanager receiver, so cleared
		// alerts arrive here too. Acknowledge them, but never remediate a
		// service that is already healthy.
		fmt.Printf("🔕 [%s] Resolved notification acknowledged (not remediating).\n",
			truncate(alert.ServiceName, 40))
		metrics.AlertsReceived.Inc()
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"resolved","action":"ignored"}`))
		return
	}
	if fromAM {
		fmt.Printf("🔔 Alertmanager alert normalized: id=%s service=%s\n",
			truncate(alert.ID, 16), truncate(alert.ServiceName, 40))
	}

	// Unmarshal payload to inject logs
	var payloadMap map[string]interface{}
	if err := json.Unmarshal(alert.Payload, &payloadMap); err == nil {
		if h.K8sClient != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			h.enrichWithK8sContext(ctx, &alert, payloadMap)
			
			// Re-marshal the enriched payload
			if enriched, err := json.Marshal(payloadMap); err == nil {
				alert.Payload = enriched
			}
		}
	}

	// Truncate payload to protect the LLM context window (Max 4096 bytes for
	// enriched RAG). Slice on a rune boundary so we never hand invalid
	// UTF-8 to the evaluator.
	if len(alert.Payload) > maxPayloadBytes {
		alert.Payload = truncateRunes(alert.Payload, maxPayloadBytes)
	}

	// Track the pending alert in the WaitGroup so we can shut down gracefully
	h.Wg.Add(1)
	metrics.AlertsReceived.Inc()
	h.AlertChan <- alert

	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"accepted"}`))
}

func (h *Handler) enrichWithK8sContext(ctx context.Context, alert *Alert, payloadMap map[string]interface{}) {
	namespace := "default"
	podName := alert.ServiceName
	var deploymentName string

	if labels, ok := payloadMap["labels"].(map[string]interface{}); ok {
		if ns, ok := labels["namespace"].(string); ok {
			namespace = ns
		}
		if p, ok := labels["pod"].(string); ok {
			podName = p
		}
		if d, ok := labels["deployment"].(string); ok {
			deploymentName = d
		}
	}

	// If no pod name is explicitly given but we have a deployment, find a pod
	if podName == alert.ServiceName && deploymentName != "" {
		pods, err := h.K8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app=%s", deploymentName),
		})
		if err == nil && len(pods.Items) > 0 {
			podName = pods.Items[0].Name
		}
	}

	// Fetch Events
	events, err := h.K8sClient.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		var relevantEvents []string
		for _, e := range events.Items {
			// Basic filtering to find events related to this service/pod/deployment
			if podName != "" && e.InvolvedObject.Name == podName || deploymentName != "" && e.InvolvedObject.Name == deploymentName {
				relevantEvents = append(relevantEvents, fmt.Sprintf("[%s] %s: %s", e.Type, e.Reason, e.Message))
			}
		}
		// Keep last 5 events
		if len(relevantEvents) > 5 {
			relevantEvents = relevantEvents[len(relevantEvents)-5:]
		}
		if len(relevantEvents) > 0 {
			payloadMap["k8s_events"] = relevantEvents
		}
	}

	// Fetch Logs
	if podName != "" {
		tailLines := int64(50)
		req := h.K8sClient.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
			TailLines: &tailLines,
		})
		
		podLogs, err := req.Stream(ctx)
		if err == nil {
			defer podLogs.Close()
			logBytes, _ := io.ReadAll(podLogs)
			if len(logBytes) > 0 {
				payloadMap["recent_logs"] = string(logBytes)
			}
		} else {
			fmt.Printf("⚠️  Could not fetch logs for pod %s in %s: %v\n", podName, namespace, err)
		}
	}
}

