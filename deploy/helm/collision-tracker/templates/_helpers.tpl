{{- define "ct.name" -}}collision-tracker{{- end -}}

{{- define "ct.fullname" -}}{{ .Release.Name }}{{- end -}}

{{- define "ct.labels" -}}
app.kubernetes.io/name: {{ include "ct.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{/* selector labels for one component: call with (dict "ctx" . "component" "api") */}}
{{- define "ct.selector" -}}
app.kubernetes.io/name: {{ include "ct.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/* image reference: call with (dict "ctx" . "image" .Values.image.collisionctl "name" "collisionctl") */}}
{{- define "ct.image" -}}
{{- if .image.digest -}}
{{ .ctx.Values.image.registry }}/{{ .name }}@{{ .image.digest }}
{{- else -}}
{{ .ctx.Values.image.registry }}/{{ .name }}:{{ .image.tag }}
{{- end -}}
{{- end -}}

{{- define "ct.podSecurity" -}}
runAsNonRoot: true
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "ct.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}

{{/* environment shared by every collisionctl process */}}
{{- define "ct.env" -}}
- name: COLLISION_DATABASE_URL
  valueFrom: { secretKeyRef: { name: {{ .Values.secrets.database.name }}, key: {{ .Values.secrets.database.key }} } }
- name: COLLISION_TEMPORAL_ADDRESS
  value: {{ .Values.temporal.address | quote }}
- name: COLLISION_TEMPORAL_NAMESPACE
  value: {{ .Values.temporal.namespace | quote }}
- name: COLLISION_TEMPORAL_SEARCH_ATTRIBUTES
  value: {{ .Values.temporal.searchAttributes | quote }}
- name: COLLISION_LOG_LEVEL
  value: {{ .Values.logLevel | quote }}
- name: COLLISION_LOG_FORMAT
  value: json
{{- end -}}

{{- define "ct.antiAffinity" -}}
{{- if .ctx.Values.podAntiAffinity }}
affinity:
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          topologyKey: kubernetes.io/hostname
          labelSelector:
            matchLabels:
              {{- include "ct.selector" . | nindent 14 }}
{{- end }}
{{- end -}}

{{- define "ct.dns" }}
- to:
    - namespaceSelector: {}
      podSelector:
        matchLabels:
          k8s-app: kube-dns
  ports:
    - { protocol: UDP, port: 53 }
    - { protocol: TCP, port: 53 }
{{- end }}
{{- define "ct.toNamespace" }}
- to:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: {{ .ns }}
  ports:
    - { protocol: TCP, port: {{ .port }} }
{{- end }}
{{- define "ct.toInternet" }}
- to:
    - ipBlock:
        cidr: 0.0.0.0/0
        except:
          {{- range .ranges }}
          - {{ . }}
          {{- end }}
  ports:
    - { protocol: TCP, port: 443 }
    - { protocol: TCP, port: 80 }
{{- end }}
