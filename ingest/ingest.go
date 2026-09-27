package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
	"context"
	
	"jev-proxy/metrics"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Alert represents an incoming infrastructure alert.
type Alert struct {
	ID          string          `json:"id"`
	ServiceName string          `json:"service_name"`
	Payload     json.RawMessage `json:"payload"`
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

	// Truncate payload to protect the LLM context window (Max 4096 bytes for enriched RAG)
	if len(alert.Payload) > 4096 {
		alert.Payload = alert.Payload[:4096]
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

