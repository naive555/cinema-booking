// Package queue publishes and consumes booking events via Redis Streams.
// TODO: implement Publish(event), StartConsumer(group, consumer), idempotent handler;
// used to decouple booking confirmation from the HTTP request path.
package queue
