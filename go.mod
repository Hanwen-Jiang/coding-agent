module github.com/agentruntime/coding-agent

go 1.26.0

require github.com/agentruntime/agent-runtime/libs/go-core v0.0.0-00010101000000-000000000000

require (
	github.com/agentruntime/agent-runtime/proto/gen/go v0.0.0-00010101000000-000000000000 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace (
	github.com/agentruntime/agent-runtime/libs/go-core => ../agent-runtime/libs/go-core
	github.com/agentruntime/agent-runtime/proto/gen/go => ../agent-runtime/proto/gen/go
)
