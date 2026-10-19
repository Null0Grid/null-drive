// This stub module exists only so the Go toolchain ignores ui/.
//
// `go build ./...` and `go test ./...` walk every subdirectory of the main
// module, including ui/node_modules, and some npm packages ship .go files.
// A go.mod here makes ui/ a separate (empty) module, which ./... skips.
// The built UI is embedded from server/internal/webui/dist instead.
module nulldrive.invalid/ui-ignore

go 1.24
