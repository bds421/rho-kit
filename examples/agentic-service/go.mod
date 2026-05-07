module github.com/bds421/rho-kit/examples/agentic-service

go 1.26.2

require (
	github.com/bds421/rho-kit/data/actionlog v0.0.0
	github.com/bds421/rho-kit/data/actionlog/memory v0.0.0
	github.com/bds421/rho-kit/data/approval v0.0.0
	github.com/bds421/rho-kit/data/approval/memory v0.0.0
	github.com/bds421/rho-kit/data/budget v0.0.0
	github.com/bds421/rho-kit/data/budget/memory v0.0.0
	github.com/bds421/rho-kit/httpx/mcp v0.0.0
	github.com/stretchr/testify v1.11.1
)

replace (
	github.com/bds421/rho-kit/data/actionlog => ../../data/actionlog
	github.com/bds421/rho-kit/data/actionlog/memory => ../../data/actionlog/memory
	github.com/bds421/rho-kit/data/approval => ../../data/approval
	github.com/bds421/rho-kit/data/approval/memory => ../../data/approval/memory
	github.com/bds421/rho-kit/data/budget => ../../data/budget
	github.com/bds421/rho-kit/data/budget/memory => ../../data/budget/memory
	github.com/bds421/rho-kit/httpx/mcp => ../../httpx/mcp
)
