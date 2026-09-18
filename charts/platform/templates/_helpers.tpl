{{/* ---------------------------------------------------------------- names */}}
{{- define "titan.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "titan.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 40 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name | trunc 40 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* component full name: titan-api, titan-kafka ... */}}
{{- define "titan.component" -}}
{{- printf "%s-%s" (include "titan.fullname" .root) .name -}}
{{- end -}}

{{/* ---------------------------------------------------------------- labels */}}
{{- define "titan.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version | replace "+" "_" }}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/part-of: titanedge
app.kubernetes.io/version: {{ .root.Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
app: {{ include "titan.component" . }}
{{- end -}}

{{- define "titan.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end -}}

{{/* Pod labels. Istio uses `app` and `version` for telemetry. */}}
{{- define "titan.podLabels" -}}
{{ include "titan.selectorLabels" . }}
app.kubernetes.io/part-of: titanedge
app: {{ include "titan.component" . }}
version: {{ .root.Values.global.imageTag | quote }}
{{- with .root.Values.global.podLabels }}
{{ toYaml . }}
{{- end }}
{{- if .noMesh }}
sidecar.istio.io/inject: "false"
{{- end }}
{{- end -}}

{{/* ---------------------------------------------------------------- images */}}
{{/* Our own images live in the global registry with the global tag. */}}
{{- define "titan.ownImage" -}}
{{- $v := .root.Values -}}
{{- $tag := .image.tag | default $v.global.imageTag -}}
{{- if $v.global.imageRegistry -}}
{{- printf "%s/%s:%s" $v.global.imageRegistry .image.repository $tag -}}
{{- else -}}
{{- printf "%s:%s" .image.repository $tag -}}
{{- end -}}
{{- end -}}

{{/* Third-party images are pulled from their public registries. */}}
{{- define "titan.image" -}}
{{- printf "%s:%s" .repository .tag -}}
{{- end -}}

{{/* ---------------------------------------------------------------- pod spec fragments */}}
{{- define "titan.podSecurity" -}}
securityContext:
  {{- toYaml .Values.podSecurityContext | nindent 2 }}
{{- with .Values.global.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{- define "titan.containerSecurity" -}}
securityContext:
  {{- toYaml .Values.containerSecurityContext | nindent 2 }}
{{- end -}}

{{- define "titan.topologySpread" -}}
{{- if .root.Values.topologySpread.enabled }}
topologySpreadConstraints:
  - maxSkew: {{ .root.Values.topologySpread.maxSkew }}
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: {{ .root.Values.topologySpread.whenUnsatisfiable }}
    labelSelector:
      matchLabels:
        {{- include "titan.selectorLabels" . | nindent 8 }}
{{- end }}
{{- end -}}

{{/* ---------------------------------------------------------------- capabilities */}}
{{- define "titan.hasMonitoring" -}}
{{- if and .Values.monitoring.enabled (.Capabilities.APIVersions.Has "monitoring.coreos.com/v1") -}}true{{- end -}}
{{- end -}}

{{- define "titan.hasKeda" -}}
{{- if and .Values.keda.enabled (.Capabilities.APIVersions.Has "keda.sh/v1alpha1") -}}true{{- end -}}
{{- end -}}

{{- define "titan.hasIstio" -}}
{{- if and .Values.istio.enabled (.Capabilities.APIVersions.Has "security.istio.io/v1") -}}true{{- end -}}
{{- end -}}

{{/* ---------------------------------------------------------------- connection info */}}
{{- define "titan.secretName" -}}
{{- if .Values.postgres.auth.existingSecret -}}
{{- .Values.postgres.auth.existingSecret -}}
{{- else -}}
{{- include "titan.component" (dict "root" . "name" "secrets") -}}
{{- end -}}
{{- end -}}

{{- define "titan.postgresHost" -}}
{{- include "titan.component" (dict "root" . "name" "postgres") -}}
{{- end -}}

{{- define "titan.redisAddr" -}}
{{- if .Values.redis.enabled -}}
{{- printf "%s:6379" (include "titan.component" (dict "root" . "name" "redis")) -}}
{{- else -}}
{{- .Values.externalRedis.addr -}}
{{- end -}}
{{- end -}}

{{- define "titan.kafkaBrokers" -}}
{{- if .Values.kafka.enabled -}}
{{- $name := include "titan.component" (dict "root" . "name" "kafka") -}}
{{- $brokers := list -}}
{{- range $i := until (int .Values.kafka.replicas) -}}
{{- $brokers = append $brokers (printf "%s-%d.%s-headless.%s.svc.cluster.local:9092" $name $i $name $.Release.Namespace) -}}
{{- end -}}
{{- join "," $brokers -}}
{{- else -}}
{{- .Values.externalKafka.brokers -}}
{{- end -}}
{{- end -}}

{{/* Environment shared by api, worker and the migrate job. */}}
{{- define "titan.backendEnv" -}}
- name: POD_NAME
  valueFrom:
    fieldRef:
      fieldPath: metadata.name
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ include "titan.secretName" . }}
      key: database-url
- name: REDIS_ADDR
  value: {{ include "titan.redisAddr" . | quote }}
- name: REDIS_TLS
  value: {{ and (not .Values.redis.enabled) .Values.externalRedis.tls | quote }}
{{- if or .Values.redis.enabled .Values.externalRedis.password }}
- name: REDIS_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "titan.component" (dict "root" . "name" "secrets") }}
      key: redis-password
{{- end }}
- name: KAFKA_BROKERS
  value: {{ include "titan.kafkaBrokers" . | quote }}
- name: KAFKA_TOPIC
  value: {{ .Values.kafka.topic | quote }}
- name: KAFKA_PARTITIONS
  value: {{ .Values.kafka.partitions | quote }}
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: {{ .Values.otel.endpoint | quote }}
- name: TRACE_SAMPLE_RATIO
  value: {{ .Values.otel.sampleRatio | quote }}
- name: OTEL_RESOURCE_ATTRIBUTES
  value: "k8s.namespace.name={{ .Release.Namespace }},deployment.environment={{ .Values.global.imageTag }}"
{{- end -}}
