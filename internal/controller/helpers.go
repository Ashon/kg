// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

package controller

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// conditionMessageLimit keeps condition messages inside the API server's limit
// while still carrying the useful part of an SSH or kubeadm error.
const conditionMessageLimit = 512

func setCondition(conditions *[]metav1.Condition, condType string, status metav1.ConditionStatus, reason, message string, generation int64) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit-3] + "..."
}

func ptr[T any](v T) *T { return &v }
