{{- define "zhiqi.image" -}}
{{- $digest := required "image.digest is required" .Values.image.digest -}}
{{- if not (regexMatch "^sha256:[a-f0-9]{64}$" $digest) -}}
{{- fail "image.digest must be a sha256 digest" -}}
{{- end -}}
{{- printf "%s@%s" (required "image.repository is required" .Values.image.repository) $digest -}}
{{- end -}}

{{- define "zhiqi.selector" -}}
app.kubernetes.io/name: zhiqi
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{- define "zhiqi.consoleSelector" -}}
app.kubernetes.io/name: zhiqi-console
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end }}

{{- define "zhiqi.podSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{- define "zhiqi.containerSecurityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: ["ALL"]
{{- end -}}
