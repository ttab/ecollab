module github.com/ttab/ecollab

go 1.27.1

require (
	connectrpc.com/connect v1.20.0
	github.com/ttab/elephant-api v0.24.2
	github.com/ttab/goyjs v0.1.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/tetratelabs/wazero v1.12.0 // indirect
	github.com/ttab/newsdoc v1.1.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
)

// goyjs carries the wasm module the Y.Doc work runs in. Pointed at
// the working copy while its ABI is still in flight; drop the
// replace and pin a tag before release.
replace github.com/ttab/goyjs => /home/hugowett/Projects/goyjs

// elephant-api carries newsdoc and the CollaborationService
// declaration. Pointed at the working copy while the declaration is
// still in flight; drop the replace and pin a tag before release.
replace github.com/ttab/elephant-api => /home/hugowett/Projects/elephant-api
