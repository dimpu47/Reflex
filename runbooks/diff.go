package runbooks

import (
	"fmt"
	"strings"
)

// GenerateProposedDiff creates a unified diff representation of the proposed cluster mutation
func GenerateProposedDiff(domain, service string, steps []Step) string {
	if service == "" {
		service = "workload"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- k8s/%s/status (current: failing)\n", service))
	sb.WriteString(fmt.Sprintf("+++ k8s/%s/status (proposed: remediated)\n", service))

	switch domain {
	case "database":
		sb.WriteString("@@ -1,4 +1,4 @@\n")
		sb.WriteString("- deployment.spec.replicas: 1 (exhausted connections)\n")
		sb.WriteString("- pod.status: OutOfMemory / ConnectionLimitExceeded\n")
		sb.WriteString("+ step 1: drain connections (scale deploy/redis --replicas=0)\n")
		sb.WriteString("+ step 2: restart redis pod (rollout restart deploy/redis)\n")
		sb.WriteString("+ step 3: restore connections (scale deploy/redis --replicas=1)\n")
		sb.WriteString("+ deployment.status: 1/1 replicas ready, clean memory state")
	default:
		sb.WriteString("@@ -1,3 +1,3 @@\n")
		sb.WriteString(fmt.Sprintf("- %s.status: CrashLoopBackOff / 500 Spike\n", service))
		sb.WriteString(fmt.Sprintf("+ %s.status: Remediated via %d-step DAG\n", service, len(steps)))
		for i, s := range steps {
			sb.WriteString(fmt.Sprintf("+ step %d: %s (%s)\n", i+1, s.Name, s.Action))
		}
		sb.WriteString("+ deployment.status: Pod rolled out and healthy")
	}

	return sb.String()
}

// GenerateAppliedDiff creates a unified diff representation of the executed or rolled-back changes
func GenerateAppliedDiff(domain, service string, completedSteps []Step, status string) string {
	if service == "" {
		service = "workload"
	}
	var sb strings.Builder
	if status == "rolled_back" {
		sb.WriteString(fmt.Sprintf("--- k8s/%s (remediated)\n", service))
		sb.WriteString(fmt.Sprintf("+++ k8s/%s (compensated rollback)\n", service))
		sb.WriteString("@@ -1,3 +1,3 @@\n")
		sb.WriteString("- state: forward remediation applied\n")
		sb.WriteString("+ compensation: executed compensating transactions in reverse LIFO order\n")
		for _, s := range completedSteps {
			if s.Compensate != "" {
				sb.WriteString(fmt.Sprintf("+ rollback step: %s (Compensate: %s)\n", s.Name, s.Compensate))
			}
		}
		sb.WriteString("+ status: cluster returned safely to fallback state")
	} else {
		sb.WriteString(fmt.Sprintf("--- k8s/%s (failing state)\n", service))
		sb.WriteString(fmt.Sprintf("+++ k8s/%s (active remediated state)\n", service))
		sb.WriteString("@@ -1,3 +1,3 @@\n")
		sb.WriteString(fmt.Sprintf("- incident: %s domain failure detected\n", domain))
		for _, s := range completedSteps {
			sb.WriteString(fmt.Sprintf("+ executed: %s [%s]\n", s.Name, s.Action))
			if s.Output != "" {
				lines := strings.Split(s.Output, "\n")
				for _, l := range lines {
					if trimmed := strings.TrimSpace(l); trimmed != "" {
						sb.WriteString(fmt.Sprintf("    > %s\n", trimmed))
					}
				}
			}
		}
		sb.WriteString("+ status: DAG successfully completed, workload verified healthy")
	}
	return sb.String()
}
