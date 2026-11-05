package api

// tprCompressEventStreams decides whether text/event-stream responses are compressed.
//
// TPR sends event streams uncompressed, as the Anthropic and OpenAI APIs do. Model streams
// sit idle for 30 seconds or more while a model writes a large tool call, then arrive in one
// burst. Through Tailscale Serve to Claude Code on Windows (Bun's fetch), zstd-compressed
// streams sometimes stalled after such a burst: the hub logged a completed 200, but the client
// never saw the rest of the message. Uncompressed streams are what clients are built and tested
// against, and the bytes saved are small next to the request bodies.
//
// Upstream compresses event streams. Its tests set this to true in TestMain
// (tpr_stream_compression_test.go) so they keep covering that path.
var tprCompressEventStreams = false
