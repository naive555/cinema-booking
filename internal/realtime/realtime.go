// Package realtime pushes seat-state changes to connected browsers over WebSocket.
// TODO: implement Hub (register/unregister/broadcast), WebSocket upgrade handler,
// fan-out of booking and lock events to clients watching a showtime.
package realtime
