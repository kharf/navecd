// Copyright 2024 kharf
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package helmtest

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/kharf/navecd/internal/gittest"
	"github.com/kharf/navecd/internal/ocitest"
	"github.com/kharf/navecd/internal/txtar"
	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/kube"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	helmKube "helm.sh/helm/v3/pkg/kube"
	helmRegistry "helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/repo"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const chartV1Template = `
-- test/Chart.yaml --
apiVersion: v2
name: test
description: A Helm chart for Kubernetes
type: application
version: 1.0.0
appVersion: "1.16.0"

-- test/crds/crontab.yaml --
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: crontabs.stable.example.com
spec:
  group: stable.example.com
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                cronSpec:
                  type: string
                image:
                  type: string
                replicas:
                  type: integer
  scope: Namespaced
  names:
    plural: crontabs
    singular: crontab
    kind: CronTab
    shortNames:
    - ct

-- test/templates/hpa.yaml --
{{- if .Values.autoscaling.enabled }}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ include "test.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {{ include "test.fullname" . }}
  minReplicas: {{ .Values.autoscaling.minReplicas }}
  maxReplicas: {{ .Values.autoscaling.maxReplicas }}
  metrics:
    {{- if .Values.autoscaling.targetCPUUtilizationPercentage }}
    - type: Resource
      resource:
        name: cpu
        target:
          averageUtilization: {{ .Values.autoscaling.targetCPUUtilizationPercentage }}
          type: Utilization
    {{- end }}
    {{- if .Values.autoscaling.targetMemoryUtilizationPercentage }}
    - type: Resource
      resource:
        name: memory
        target:
          averageUtilization: {{ .Values.autoscaling.targetMemoryUtilizationPercentage }}
          type: Utilization
    {{- end }}
{{- end }}

-- test/templates/ingress.yaml --
{{- if .Values.ingress.enabled -}}
{{- $fullName := include "test.fullname" . -}}
{{- $svcPort := .Values.service.port -}}
{{- if and .Values.ingress.className (not (semverCompare ">=1.18-0" .Capabilities.KubeVersion.GitVersion)) }}
  {{- if not (hasKey .Values.ingress.annotations "kubernetes.io/ingress.class") }}
  {{- $_ := set .Values.ingress.annotations "kubernetes.io/ingress.class" .Values.ingress.className}}
  {{- end }}
{{- end }}
{{- if semverCompare ">=1.19-0" .Capabilities.KubeVersion.GitVersion -}}
apiVersion: networking.k8s.io/v1
{{- else if semverCompare ">=1.14-0" .Capabilities.KubeVersion.GitVersion -}}
apiVersion: networking.k8s.io/v1beta1
{{- else -}}
apiVersion: extensions/v1beta1
{{- end }}
kind: Ingress
metadata:
  name: {{ $fullName }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
  {{- with .Values.ingress.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  {{- if and .Values.ingress.className (semverCompare ">=1.18-0" .Capabilities.KubeVersion.GitVersion) }}
  ingressClassName: {{ .Values.ingress.className }}
  {{- end }}
  {{- if .Values.ingress.tls }}
  tls:
    {{- range .Values.ingress.tls }}
    - hosts:
        {{- range .hosts }}
        - {{ . | quote }}
        {{- end }}
      secretName: {{ .secretName }}
    {{- end }}
  {{- end }}
  rules:
    {{- range .Values.ingress.hosts }}
    - host: {{ .host | quote }}
      http:
        paths:
          {{- range .paths }}
          - path: {{ .path }}
            {{- if and .pathType (semverCompare ">=1.18-0" $.Capabilities.KubeVersion.GitVersion) }}
            pathType: {{ .pathType }}
            {{- end }}
            backend:
              {{- if semverCompare ">=1.19-0" $.Capabilities.KubeVersion.GitVersion }}
              service:
                name: {{ $fullName }}
                port:
                  number: {{ $svcPort }}
              {{- else }}
              serviceName: {{ $fullName }}
              servicePort: {{ $svcPort }}
              {{- end }}
          {{- end }}
    {{- end }}
{{- end }}

-- test/templates/service.yaml --
apiVersion: v1
kind: Service
metadata:
  name: {{ include "test.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  ports:
    - port: {{ .Values.service.port }}
      targetPort: http
      protocol: TCP
      name: http
  selector:
    {{- include "test.selectorLabels" . | nindent 4 }}

-- test/templates/deployment.yaml --
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "test.fullname" . }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
spec:
  {{- if not .Values.autoscaling.enabled }}
  replicas: {{ .Values.replicaCount }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "test.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      {{- with .Values.podAnnotations }}
      annotations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      labels:
        {{- include "test.selectorLabels" . | nindent 8 }}
    spec:
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "test.serviceAccountName" . }}
      securityContext:
        {{- toYaml .Values.podSecurityContext | nindent 8 }}
      containers:
        - name: {{ .Chart.Name }}
          securityContext:
            {{- toYaml .Values.securityContext | nindent 12 }}
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          ports:
            - name: http
              containerPort: {{ .Values.service.port }}
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /
              port: http
          readinessProbe:
            httpGet:
              path: /
              port: http
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}

-- test/templates/serviceaccount.yaml --
{{- if .Values.serviceAccount.create -}}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "test.serviceAccountName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
  {{- with .Values.serviceAccount.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}

-- test/templates/tests/test-connection.yaml --
apiVersion: v1
kind: Pod
metadata:
  name: "{{ include "test.fullname" . }}-test-connection"
  labels:
    {{- include "test.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": test
spec:
  containers:
    - name: wget
      image: busybox
      command: ['wget']
      args: ['{{ include "test.fullname" . }}:{{ .Values.service.port }}']
  restartPolicy: Never

-- test/templates/_helpers.tpl --
{{/*
Expand the name of the chart.
*/}}
{{- define "test.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "test.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "test.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "test.labels" -}}
helm.sh/chart: {{ include "test.chart" . }}
{{ include "test.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "test.selectorLabels" -}}
app.kubernetes.io/name: {{ include "test.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "test.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "test.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

-- test/values.yaml --
# Default values for test.
# This is a YAML-formatted file.
# Declare variables to be passed into your templates.

replicaCount: 1

image:
  repository: nginx
  pullPolicy: IfNotPresent
  # Overrides the image tag whose default is the chart appVersion.
  tag: ""

imagePullSecrets: []
nameOverride: ""
fullnameOverride: ""

serviceAccount:
  # Specifies whether a service account should be created
  create: true
  # Annotations to add to the service account
  annotations: {}
  # The name of the service account to use.
  # If not set and create is true, a name is generated using the fullname template
  name: ""

podAnnotations: {}

podSecurityContext: {}
  # fsGroup: 2000

securityContext: {}
  # capabilities:
  #   drop:
  #   - ALL
  # readOnlyRootFilesystem: true
  # runAsNonRoot: true
  # runAsUser: 1000

service:
  type: ClusterIP
  port: 80

ingress:
  enabled: false
  className: ""
  annotations: {}
    # kubernetes.io/ingress.class: nginx
    # kubernetes.io/tls-acme: "true"
  hosts:
    - host: chart-example.local
      paths:
        - path: /
          pathType: ImplementationSpecific
  tls: []
  #  - secretName: chart-example-tls
  #    hosts:
  #      - chart-example.local

resources: {}
  # We usually recommend not to specify default resources and to leave this as a conscious
  # choice for the user. This also increases chances charts run on environments with little
  # resources, such as Minikube. If you do want to specify resources, uncomment the following
  # lines, adjust them as necessary, and remove the curly braces after 'resources:'.
  # limits:
  #   cpu: 100m
  #   memory: 128Mi
  # requests:
  #   cpu: 100m
  #   memory: 128Mi

autoscaling:
  enabled: false
  minReplicas: 1
  maxReplicas: 100
  targetCPUUtilizationPercentage: 80
  # targetMemoryUtilizationPercentage: 80

nodeSelector: {}

tolerations: []

affinity: {}
`

const chartV2Template = `
-- test/Chart.yaml --
apiVersion: v2
name: test
description: A Helm chart for Kubernetes
type: application
version: 2.0.0
appVersion: "1.16.0"

-- test/crds/crontab.yaml --
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: crontabs.stable.example.com
spec:
  group: stable.example.com
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                cronSpec:
                  type: string
                image:
                  type: string
                replicas:
                  type: integer
  scope: Namespaced
  names:
    plural: crontabs
    singular: crontab
    kind: CronTab
    shortNames:
    - ct

-- test/templates/hpa.yaml --
{{- if .Values.autoscaling.enabled }}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ include "test.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {{ include "test.fullname" . }}
  minReplicas: {{ .Values.autoscaling.minReplicas }}
  maxReplicas: {{ .Values.autoscaling.maxReplicas }}
  metrics:
    {{- if .Values.autoscaling.targetCPUUtilizationPercentage }}
    - type: Resource
      resource:
        name: cpu
        target:
          averageUtilization: {{ .Values.autoscaling.targetCPUUtilizationPercentage }}
          type: Utilization
    {{- end }}
    {{- if .Values.autoscaling.targetMemoryUtilizationPercentage }}
    - type: Resource
      resource:
        name: memory
        target:
          averageUtilization: {{ .Values.autoscaling.targetMemoryUtilizationPercentage }}
          type: Utilization
    {{- end }}
{{- end }}

-- test/templates/ingress.yaml --
{{- if .Values.ingress.enabled -}}
{{- $fullName := include "test.fullname" . -}}
{{- $svcPort := .Values.service.port -}}
{{- if and .Values.ingress.className (not (semverCompare ">=1.18-0" .Capabilities.KubeVersion.GitVersion)) }}
  {{- if not (hasKey .Values.ingress.annotations "kubernetes.io/ingress.class") }}
  {{- $_ := set .Values.ingress.annotations "kubernetes.io/ingress.class" .Values.ingress.className}}
  {{- end }}
{{- end }}
{{- if semverCompare ">=1.19-0" .Capabilities.KubeVersion.GitVersion -}}
apiVersion: networking.k8s.io/v1
{{- else if semverCompare ">=1.14-0" .Capabilities.KubeVersion.GitVersion -}}
apiVersion: networking.k8s.io/v1beta1
{{- else -}}
apiVersion: extensions/v1beta1
{{- end }}
kind: Ingress
metadata:
  name: {{ $fullName }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
  {{- with .Values.ingress.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  {{- if and .Values.ingress.className (semverCompare ">=1.18-0" .Capabilities.KubeVersion.GitVersion) }}
  ingressClassName: {{ .Values.ingress.className }}
  {{- end }}
  {{- if .Values.ingress.tls }}
  tls:
    {{- range .Values.ingress.tls }}
    - hosts:
        {{- range .hosts }}
        - {{ . | quote }}
        {{- end }}
      secretName: {{ .secretName }}
    {{- end }}
  {{- end }}
  rules:
    {{- range .Values.ingress.hosts }}
    - host: {{ .host | quote }}
      http:
        paths:
          {{- range .paths }}
          - path: {{ .path }}
            {{- if and .pathType (semverCompare ">=1.18-0" $.Capabilities.KubeVersion.GitVersion) }}
            pathType: {{ .pathType }}
            {{- end }}
            backend:
              {{- if semverCompare ">=1.19-0" $.Capabilities.KubeVersion.GitVersion }}
              service:
                name: {{ $fullName }}
                port:
                  number: {{ $svcPort }}
              {{- else }}
              serviceName: {{ $fullName }}
              servicePort: {{ $svcPort }}
              {{- end }}
          {{- end }}
    {{- end }}
{{- end }}

-- test/templates/service.yaml --
apiVersion: v1
kind: Service
metadata:
  name: {{ include "test.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  ports:
    - port: {{ .Values.service.port }}
      targetPort: http
      protocol: TCP
      name: http
  selector:
    {{- include "test.selectorLabels" . | nindent 4 }}

-- test/templates/deployment.yaml --
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "test.fullname" . }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
spec:
  {{- if not .Values.autoscaling.enabled }}
  replicas: {{ .Values.replicaCount }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "test.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      {{- with .Values.podAnnotations }}
      annotations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      labels:
        {{- include "test.selectorLabels" . | nindent 8 }}
    spec:
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "test.serviceAccountName" . }}
      securityContext:
        {{- toYaml .Values.podSecurityContext | nindent 8 }}
      containers:
        - name: {{ .Chart.Name }}
          securityContext:
            {{- toYaml .Values.securityContext | nindent 12 }}
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          ports:
            - name: http
              containerPort: {{ .Values.service.port }}
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /
              port: http
          readinessProbe:
            httpGet:
              path: /
              port: http
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}

-- test/templates/tests/test-connection.yaml --
apiVersion: v1
kind: Pod
metadata:
  name: "{{ include "test.fullname" . }}-test-connection"
  labels:
    {{- include "test.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": test
spec:
  containers:
    - name: wget
      image: busybox
      command: ['wget']
      args: ['{{ include "test.fullname" . }}:{{ .Values.service.port }}']
  restartPolicy: Never

-- test/templates/_helpers.tpl --
{{/*
Expand the name of the chart.
*/}}
{{- define "test.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "test.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "test.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "test.labels" -}}
helm.sh/chart: {{ include "test.chart" . }}
{{ include "test.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "test.selectorLabels" -}}
app.kubernetes.io/name: {{ include "test.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "test.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "test.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

-- test/values.yaml --
# Default values for test.
# This is a YAML-formatted file.
# Declare variables to be passed into your templates.

replicaCount: 1

image:
  repository: nginx
  pullPolicy: IfNotPresent
  # Overrides the image tag whose default is the chart appVersion.
  tag: ""

imagePullSecrets: []
nameOverride: ""
fullnameOverride: ""

serviceAccount:
  # Specifies whether a service account should be created
  create: true
  # Annotations to add to the service account
  annotations: {}
  # The name of the service account to use.
  # If not set and create is true, a name is generated using the fullname template
  name: ""

podAnnotations: {}

podSecurityContext: {}
  # fsGroup: 2000

securityContext: {}
  # capabilities:
  #   drop:
  #   - ALL
  # readOnlyRootFilesystem: true
  # runAsNonRoot: true
  # runAsUser: 1000

service:
  type: ClusterIP
  port: 80

ingress:
  enabled: false
  className: ""
  annotations: {}
    # kubernetes.io/ingress.class: nginx
    # kubernetes.io/tls-acme: "true"
  hosts:
    - host: chart-example.local
      paths:
        - path: /
          pathType: ImplementationSpecific
  tls: []
  #  - secretName: chart-example-tls
  #    hosts:
  #      - chart-example.local

resources: {}
  # We usually recommend not to specify default resources and to leave this as a conscious
  # choice for the user. This also increases chances charts run on environments with little
  # resources, such as Minikube. If you do want to specify resources, uncomment the following
  # lines, adjust them as necessary, and remove the curly braces after 'resources:'.
  # limits:
  #   cpu: 100m
  #   memory: 128Mi
  # requests:
  #   cpu: 100m
  #   memory: 128Mi

autoscaling:
  enabled: false
  minReplicas: 1
  maxReplicas: 100
  targetCPUUtilizationPercentage: 80
  # targetMemoryUtilizationPercentage: 80

nodeSelector: {}

tolerations: []

affinity: {}
`

const chartV3Template = `
-- test/Chart.yaml --
apiVersion: v2
name: test
description: A Helm chart for Kubernetes
type: application
version: 3.0.0
appVersion: "1.16.0"

-- test/crds/crontab.yaml --
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: crontabs.stable.example.com
spec:
  group: stable.example.com
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                cronSpec:
                  type: string
                image:
                  type: string
  scope: Namespaced
  names:
    plural: crontabs
    singular: crontab
    kind: CronTab
    shortNames:
    - ct

-- test/templates/hpa.yaml --
{{- if .Values.autoscaling.enabled }}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ include "test.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {{ include "test.fullname" . }}
  minReplicas: {{ .Values.autoscaling.minReplicas }}
  maxReplicas: {{ .Values.autoscaling.maxReplicas }}
  metrics:
    {{- if .Values.autoscaling.targetCPUUtilizationPercentage }}
    - type: Resource
      resource:
        name: cpu
        target:
          averageUtilization: {{ .Values.autoscaling.targetCPUUtilizationPercentage }}
          type: Utilization
    {{- end }}
    {{- if .Values.autoscaling.targetMemoryUtilizationPercentage }}
    - type: Resource
      resource:
        name: memory
        target:
          averageUtilization: {{ .Values.autoscaling.targetMemoryUtilizationPercentage }}
          type: Utilization
    {{- end }}
{{- end }}

-- test/templates/ingress.yaml --
{{- if .Values.ingress.enabled -}}
{{- $fullName := include "test.fullname" . -}}
{{- $svcPort := .Values.service.port -}}
{{- if and .Values.ingress.className (not (semverCompare ">=1.18-0" .Capabilities.KubeVersion.GitVersion)) }}
  {{- if not (hasKey .Values.ingress.annotations "kubernetes.io/ingress.class") }}
  {{- $_ := set .Values.ingress.annotations "kubernetes.io/ingress.class" .Values.ingress.className}}
  {{- end }}
{{- end }}
{{- if semverCompare ">=1.19-0" .Capabilities.KubeVersion.GitVersion -}}
apiVersion: networking.k8s.io/v1
{{- else if semverCompare ">=1.14-0" .Capabilities.KubeVersion.GitVersion -}}
apiVersion: networking.k8s.io/v1beta1
{{- else -}}
apiVersion: extensions/v1beta1
{{- end }}
kind: Ingress
metadata:
  name: {{ $fullName }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
  {{- with .Values.ingress.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  {{- if and .Values.ingress.className (semverCompare ">=1.18-0" .Capabilities.KubeVersion.GitVersion) }}
  ingressClassName: {{ .Values.ingress.className }}
  {{- end }}
  {{- if .Values.ingress.tls }}
  tls:
    {{- range .Values.ingress.tls }}
    - hosts:
        {{- range .hosts }}
        - {{ . | quote }}
        {{- end }}
      secretName: {{ .secretName }}
    {{- end }}
  {{- end }}
  rules:
    {{- range .Values.ingress.hosts }}
    - host: {{ .host | quote }}
      http:
        paths:
          {{- range .paths }}
          - path: {{ .path }}
            {{- if and .pathType (semverCompare ">=1.18-0" $.Capabilities.KubeVersion.GitVersion) }}
            pathType: {{ .pathType }}
            {{- end }}
            backend:
              {{- if semverCompare ">=1.19-0" $.Capabilities.KubeVersion.GitVersion }}
              service:
                name: {{ $fullName }}
                port:
                  number: {{ $svcPort }}
              {{- else }}
              serviceName: {{ $fullName }}
              servicePort: {{ $svcPort }}
              {{- end }}
          {{- end }}
    {{- end }}
{{- end }}

-- test/templates/service.yaml --
apiVersion: v1
kind: Service
metadata:
  name: {{ include "test.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
spec:
  type: {{ .Values.service.type }}
  ports:
    - port: {{ .Values.service.port }}
      targetPort: http
      protocol: TCP
      name: http
  selector:
    {{- include "test.selectorLabels" . | nindent 4 }}

-- test/templates/deployment.yaml --
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "test.fullname" . }}
  labels:
    {{- include "test.labels" . | nindent 4 }}
spec:
  {{- if not .Values.autoscaling.enabled }}
  replicas: {{ .Values.replicaCount }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "test.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      {{- with .Values.podAnnotations }}
      annotations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      labels:
        {{- include "test.selectorLabels" . | nindent 8 }}
    spec:
      {{- with .Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      serviceAccountName: {{ include "test.serviceAccountName" . }}
      securityContext:
        {{- toYaml .Values.podSecurityContext | nindent 8 }}
      containers:
        - name: {{ .Chart.Name }}
          securityContext:
            {{- toYaml .Values.securityContext | nindent 12 }}
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          ports:
            - name: http
              containerPort: {{ .Values.service.port }}
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /
              port: http
          readinessProbe:
            httpGet:
              path: /
              port: http
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}

-- test/templates/tests/test-connection.yaml --
apiVersion: v1
kind: Pod
metadata:
  name: "{{ include "test.fullname" . }}-test-connection"
  labels:
    {{- include "test.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": test
spec:
  containers:
    - name: wget
      image: busybox
      command: ['wget']
      args: ['{{ include "test.fullname" . }}:{{ .Values.service.port }}']
  restartPolicy: Never

-- test/templates/_helpers.tpl --
{{/*
Expand the name of the chart.
*/}}
{{- define "test.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "test.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "test.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "test.labels" -}}
helm.sh/chart: {{ include "test.chart" . }}
{{ include "test.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "test.selectorLabels" -}}
app.kubernetes.io/name: {{ include "test.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "test.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "test.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

-- test/values.yaml --
# Default values for test.
# This is a YAML-formatted file.
# Declare variables to be passed into your templates.

replicaCount: 1

image:
  repository: nginx
  pullPolicy: IfNotPresent
  # Overrides the image tag whose default is the chart appVersion.
  tag: ""

imagePullSecrets: []
nameOverride: ""
fullnameOverride: ""

serviceAccount:
  # Specifies whether a service account should be created
  create: true
  # Annotations to add to the service account
  annotations: {}
  # The name of the service account to use.
  # If not set and create is true, a name is generated using the fullname template
  name: ""

podAnnotations: {}

podSecurityContext: {}
  # fsGroup: 2000

securityContext: {}
  # capabilities:
  #   drop:
  #   - ALL
  # readOnlyRootFilesystem: true
  # runAsNonRoot: true
  # runAsUser: 1000

service:
  type: ClusterIP
  port: 80

ingress:
  enabled: false
  className: ""
  annotations: {}
    # kubernetes.io/ingress.class: nginx
    # kubernetes.io/tls-acme: "true"
  hosts:
    - host: chart-example.local
      paths:
        - path: /
          pathType: ImplementationSpecific
  tls: []
  #  - secretName: chart-example-tls
  #    hosts:
  #      - chart-example.local

resources: {}
  # We usually recommend not to specify default resources and to leave this as a conscious
  # choice for the user. This also increases chances charts run on environments with little
  # resources, such as Minikube. If you do want to specify resources, uncomment the following
  # lines, adjust them as necessary, and remove the curly braces after 'resources:'.
  # limits:
  #   cpu: 100m
  #   memory: 128Mi
  # requests:
  #   cpu: 100m
  #   memory: 128Mi

autoscaling:
  enabled: false
  minReplicas: 1
  maxReplicas: 100
  targetCPUUtilizationPercentage: 80
  # targetMemoryUtilizationPercentage: 80

nodeSelector: {}

tolerations: []

affinity: {}
`

func ConfigureHelm(cfg *rest.Config) (*action.Configuration, error) {
	helmCfg := action.Configuration{}
	helmKube.ManagedFieldsManager = "controller"

	k8sClient, err := kube.NewExtendedDynamicClient(cfg)
	if err != nil {
		return nil, err
	}

	getter := &kube.InMemoryRESTClientGetter{
		Cfg:        cfg,
		RestMapper: k8sClient.RESTMapper(),
	}

	err = helmCfg.Init(getter, "default", "secret", log.Printf)
	if err != nil {
		return nil, err
	}

	helmCfg.KubeClient = &helm.Client{
		Client:        helmCfg.KubeClient.(*helmKube.Client),
		DynamicClient: k8sClient,
		FieldManager:  "controller",
	}

	return &helmCfg, nil
}

type projectOption struct {
	repo        *gittest.LocalGitRepository
	testProject string
	testRoot    string
}

var _ Option = (*projectOption)(nil)

func (opt projectOption) Apply(opts *options) {
	opts.project = opt
}

type enabled bool

var _ Option = (*enabled)(nil)

func (opt enabled) Apply(opts *options) {
	opts.enabled = bool(opt)
}

type oci bool

var _ Option = (*oci)(nil)

func (opt oci) Apply(opts *options) {
	opts.oci = bool(opt)
}

type private bool

var _ Option = (*private)(nil)

func (opt private) Apply(opts *options) {
	opts.private = bool(opt)
}

type provider cloud.ProviderID

var _ Option = (*provider)(nil)

func (opt provider) Apply(opts *options) {
	opts.cloudProviderID = cloud.ProviderID(opt)
}

type digest string

var _ Option = (*digest)(nil)

func (opt digest) Apply(opts *options) {
	opts.digest = string(opt)
}

type options struct {
	enabled         bool
	oci             bool
	private         bool
	project         projectOption
	cloudProviderID cloud.ProviderID
	digest          string
}

type Option interface {
	Apply(*options)
}

func Enabled(isEnabled bool) enabled {
	return enabled(isEnabled)
}

func WithOCI(enabled bool) oci {
	return oci(enabled)
}

func WithPrivate(enabled bool) private {
	return private(enabled)
}

func WithProject(
	repo *gittest.LocalGitRepository,
	testProject string,
	testRoot string,
) projectOption {
	return projectOption{
		repo:        repo,
		testProject: testProject,
		testRoot:    testRoot,
	}
}

func WithProvider(providerID cloud.ProviderID) provider {
	return provider(providerID)
}

func WithDigest(dig string) digest {
	return digest(dig)
}

type Server interface {
	// base URL of form http://ipaddr:port with no trailing slash
	URL() string
	Addr() string
	Close()
}

type OciRegistry struct {
	Server *ocitest.Registry
}

var _ Server = (*OciRegistry)(nil)

func (r *OciRegistry) Close() {
	r.Server.Close()
}

func (r *OciRegistry) URL() string {
	return r.Server.URL()
}

func (r *OciRegistry) Addr() string {
	return r.Server.Addr()
}

type yamlBasedRepository struct {
	server *httptest.Server
}

var _ Server = (*yamlBasedRepository)(nil)

func (r *yamlBasedRepository) Close() {
	r.server.Close()
}

func (r *yamlBasedRepository) URL() string {
	return r.server.URL
}

func (r *yamlBasedRepository) Addr() string {
	return r.server.Config.Addr
}

type Environment struct {
	ChartServer   Server
	chartArchives []*os.File
	V1Digest      string
}

func (env Environment) Close() {
	if env.ChartServer != nil {
		env.ChartServer.Close()
	}
	for _, f := range env.chartArchives {
		os.Remove(f.Name())
	}
}

// NewHelmEnvironment creates Helm chart archives and starts either and oci or yaml based Helm repository.
func NewHelmEnvironment(t testing.TB, opts ...Option) (*Environment, error) {
	options := &options{
		enabled:         false,
		private:         false,
		oci:             false,
		cloudProviderID: "",
		digest:          "",
	}
	for _, o := range opts {
		o.Apply(options)
	}

	v1Archive, err := createChartV1Archive(t)
	if err != nil {
		return nil, err
	}

	v2Archive, err := createChartV2Archive(t)
	if err != nil {
		return nil, err
	}

	v3Archive, err := createChartV3Archive(t)
	if err != nil {
		return nil, err
	}

	var chartServer Server
	var v1Digest string
	if options.oci {
		var err error
		ociServer, err := ocitest.NewTLSRegistry(
			options.private,
			options.cloudProviderID,
		)
		if err != nil {
			return nil, err
		}

		helmOpts := []helmRegistry.ClientOption{
			helmRegistry.ClientOptDebug(true),
			helmRegistry.ClientOptWriter(os.Stderr),
			helmRegistry.ClientOptHTTPClient(ociServer.Client()),
			helmRegistry.ClientOptResolver(nil),
		}

		helmRegistryClient, err := helmRegistry.NewClient(helmOpts...)
		if err != nil {
			return nil, err
		}

		var username, pw string
		switch options.cloudProviderID {
		case cloud.GCP:
			username = "oauth2accesstoken"
			pw = "aaaa"
		case cloud.Azure:
			username = "00000000-0000-0000-0000-000000000000"
			pw = "aaaa"
		default:
			username = "navecd"
			pw = "abcd"
		}
		err = helmRegistryClient.Login(
			ociServer.Addr(),
			helmRegistry.LoginOptBasicAuth(username, pw),
		)
		if err != nil {
			return nil, err
		}

		v1Bytes, err := os.ReadFile(v1Archive.Name())
		if err != nil {
			return nil, err
		}

		v2Bytes, err := os.ReadFile(v2Archive.Name())
		if err != nil {
			return nil, err
		}

		v3Bytes, err := os.ReadFile(v3Archive.Name())
		if err != nil {
			return nil, err
		}

		version := "1.0.0"
		result, err := helmRegistryClient.Push(
			v1Bytes,
			fmt.Sprintf("%s/%s:%s", ociServer.Addr(), "test", version),
		)
		if err != nil {
			return nil, err
		}

		v1Digest = result.Manifest.Digest

		version = "2.0.0"
		_, err = helmRegistryClient.Push(
			v2Bytes,
			fmt.Sprintf("%s/%s:%s", ociServer.Addr(), "test", version),
		)
		if err != nil {
			return nil, err
		}

		version = "3.0.0"
		_, err = helmRegistryClient.Push(
			v3Bytes,
			fmt.Sprintf("%s/%s:%s", ociServer.Addr(), "test", version),
		)
		if err != nil {
			return nil, err
		}

		chartServer = &OciRegistry{
			Server: ociServer,
		}
	} else {
		httpsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if options.private {
				auth, found := r.Header["Authorization"]
				if !found {
					w.WriteHeader(500)
					return
				}

				if len(auth) != 1 {
					w.WriteHeader(500)
					return
				}

				// navecd:abcd
				if auth[0] != "Basic bmF2ZWNkOmFiY2Q=" {
					w.WriteHeader(500)
					return
				}
			}

			if strings.HasSuffix(r.URL.Path, "index.yaml") {
				v1Digest = options.digest
				version1 := "1.0.0"
				version2 := "2.0.0"
				version3 := "3.0.0"
				index := &repo.IndexFile{
					APIVersion: "v1",
					Generated:  time.Now(),
					Entries: map[string]repo.ChartVersions{
						"test": {
							&repo.ChartVersion{
								Digest: options.digest,
								Metadata: &chart.Metadata{
									APIVersion: "v1",
									Version:    "1.0.0",
									Name:       "test",
								},
								URLs: []string{chartServer.URL() + fmt.Sprintf("/test-%s.tgz", version1)},
							},
							&repo.ChartVersion{
								Digest: options.digest,
								Metadata: &chart.Metadata{
									APIVersion: "v1",
									Version:    "2.0.0",
									Name:       "test",
								},
								URLs: []string{chartServer.URL() + fmt.Sprintf("/test-%s.tgz", version2)},
							},
							&repo.ChartVersion{
								Digest: options.digest,
								Metadata: &chart.Metadata{
									APIVersion: "v1",
									Version:    "3.0.0",
									Name:       "test",
								},
								URLs: []string{chartServer.URL() + fmt.Sprintf("/test-%s.tgz", version3)},
							},
						},
					},
				}

				indexBytes, err := yaml.Marshal(index)
				if err != nil {
					w.WriteHeader(500)
					return
				}

				if _, err := w.Write(indexBytes); err != nil {
					w.WriteHeader(500)
					return
				}

				return
			}
			archive := v1Archive
			switch {
			case strings.Contains(r.URL.Path, "2.0.0"):
				archive = v2Archive
			case strings.Contains(r.URL.Path, "3.0.0"):
				archive = v3Archive
			}

			w.Header().Set("Content-Type", "application/gzip")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filepath.Base(archive.Name())))

			file, err := os.Open(archive.Name())
			if err != nil {
				w.WriteHeader(500)
				return
			}

			if _, err := io.Copy(w, file); err != nil {
				w.WriteHeader(500)
				return
			}
		}))

		chartServer = &yamlBasedRepository{
			server: httpsServer,
		}
	}

	return &Environment{
		ChartServer:   chartServer,
		chartArchives: []*os.File{v1Archive, v2Archive},
		V1Digest:      v1Digest,
	}, nil
}

func createChartV1Archive(t testing.TB) (*os.File, error) {
	version := "1.0.0"
	archive, err := os.CreateTemp(t.TempDir(), fmt.Sprintf("*-test-%s.tgz", version))
	if err != nil {
		return nil, err
	}
	chartDir := t.TempDir()
	_, err = txtar.Create(chartDir, strings.NewReader(chartV1Template))
	if err != nil {
		return nil, err
	}

	return createChartArchive(archive, chartDir)
}

func createChartV2Archive(t testing.TB) (*os.File, error) {
	version := "2.0.0"
	archive, err := os.CreateTemp(t.TempDir(), fmt.Sprintf("*-test-%s.tgz", version))
	if err != nil {
		return nil, err
	}
	chartDir := t.TempDir()
	_, err = txtar.Create(chartDir, strings.NewReader(chartV2Template))
	if err != nil {
		return nil, err
	}

	return createChartArchive(archive, chartDir)
}

func createChartV3Archive(t testing.TB) (*os.File, error) {
	version := "3.0.0"
	archive, err := os.CreateTemp(t.TempDir(), fmt.Sprintf("*-test-%s.tgz", version))
	if err != nil {
		return nil, err
	}
	chartDir := t.TempDir()
	_, err = txtar.Create(chartDir, strings.NewReader(chartV3Template))
	if err != nil {
		return nil, err
	}

	return createChartArchive(archive, chartDir)
}

func createChartArchive(archive *os.File, chartDir string) (*os.File, error) {
	gzWriter := gzip.NewWriter(archive)
	tarWriter := tar.NewWriter(gzWriter)
	walkDirErr := filepath.WalkDir(
		chartDir,
		func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if d.IsDir() || path == ".helmignore" {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return err
			}

			relPath, err := filepath.Rel(chartDir, path)
			if err != nil {
				return err
			}
			header := &tar.Header{
				Name: relPath,
				Mode: int64(info.Mode()),
				Size: info.Size(),
			}

			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}

			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			if _, err := io.Copy(tarWriter, file); err != nil {
				return err
			}

			return nil
		},
	)
	err := tarWriter.Close()
	if err != nil {
		return nil, err
	}
	err = gzWriter.Close()
	if err != nil {
		return nil, err
	}
	if walkDirErr != nil {
		return nil, err
	}
	return archive, nil
}

type Template struct {
	TestProjectPath         string
	RelativeReleaseFilePath string
	Name                    string
	RepoURL                 string
}

func ReplaceTemplate(
	tmpl Template,
	gitRepository *gittest.LocalGitRepository,
) error {
	releasesFilePath := filepath.Join(
		tmpl.TestProjectPath,
		tmpl.RelativeReleaseFilePath,
	)

	releasesContent, err := os.ReadFile(releasesFilePath)
	if err != nil {
		return err
	}

	parsedTemplate, err := template.New("releases").Parse(string(releasesContent))
	if err != nil {
		return err
	}

	releasesFile, err := os.Create(releasesFilePath)
	if err != nil {
		return err
	}
	defer releasesFile.Close()

	err = parsedTemplate.Execute(releasesFile, struct {
		Name    string
		RepoURL string
	}{
		Name:    tmpl.Name,
		RepoURL: tmpl.RepoURL,
	})
	if err != nil {
		return err
	}

	_, err = gitRepository.CommitFile(
		tmpl.RelativeReleaseFilePath,
		"overwrite template",
	)
	if err != nil {
		return err
	}

	return nil
}
