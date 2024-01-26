{{/*
Generate dockerconfigjson from username and password
Usage {{ include "secrets.dockerconfigjson" . }}
*/}}
{{- define "secrets.dockerconfigjson" -}}
{{- with .Values.dockerSecrets -}}
auths:
  {{ required "Valid dockerSecrets.hostname is required!" .hostname }}:
    username: {{ required "Valid dockerSecrets.username is required!" .username }}
    password: {{ required "Valid dockerSecrets.password is required!" .password }}
    email: {{ .email }}
{{- end -}}
{{- end -}}

{{/*
Take secretName from env variables or use default "image-pull-secret"
*/}}
{{- define "secrets.secretName" -}}
{{- $secretName := "" -}}
{{- range .Values.env -}}
{{- if and (eq "CONFIG_SECRETNAME" .name) (ne "" .value) -}}
{{- $secretName = .value -}}
{{- end -}}
{{- end -}}
{{- default "image-pull-secret" $secretName -}}
{{- end -}}