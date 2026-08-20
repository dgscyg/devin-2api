module github.com/dgscyg/devin-2api

go 1.27.0

require (
	connectrpc.com/connect v1.20.0
	github.com/go-chi/chi/v5 v5.3.1
	github.com/gorilla/websocket v1.5.3
	github.com/jhump/protoreflect/v2 v2.0.0-beta.1
	golang.org/x/net v0.57.0
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
	local/devinproto v0.0.0
)

require github.com/kr/text v0.2.0 // indirect

replace local/devinproto => ./outputs/devin-proto-go
