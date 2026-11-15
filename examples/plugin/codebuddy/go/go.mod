module github.com/router-for-me/CLIProxyAPI/v8/examples/plugin/codebuddy/go

go 1.26.10

require (
	github.com/router-for-me/CLIProxyAPI/v8 v8.0.0
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/MoeForge/moego v0.1.0-alpha.3 // indirect

replace github.com/router-for-me/CLIProxyAPI/v8 => ../../../..
