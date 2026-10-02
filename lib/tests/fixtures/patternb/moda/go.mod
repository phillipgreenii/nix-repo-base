module example.com/moda

go 1.25.0

require (
	example.com/modb v0.0.0
	github.com/spf13/pflag v1.0.9
)

replace example.com/modb => ../modb
