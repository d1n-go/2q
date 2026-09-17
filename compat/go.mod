// Module compat holds differential tests that run the fork side by side
// with the archived upstream modules it replaced. It is a separate module
// so that the root github.com/d1n-go/2q keeps zero dependencies.
module github.com/d1n-go/2q/compat

go 1.23

replace github.com/d1n-go/2q => ../

require (
	github.com/d1n-go/2q v0.0.0-00010101000000-000000000000
	github.com/floatdrop/2q v0.1.1
)

require (
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/floatdrop/fifo v0.1.1 // indirect
	github.com/floatdrop/lru v1.2.1 // indirect
)
