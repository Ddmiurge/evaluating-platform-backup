// Module boundary marker: the engine is a Node/TypeScript service. This
// go.mod exists solely to exclude promptfoo_engine/node_modules (which ships
// promptfoo's own demo Go wrappers that do not compile) from the parent
// evaluating_platform Go module's ./... globs.
module engine.node

go 1.25
