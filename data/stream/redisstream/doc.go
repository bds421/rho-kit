// Package stream provides Redis Streams producer and consumer helpers.
//
// Stream message headers are validated before publish so directly constructed
// Message values follow the same metadata contract as values built with
// Message.WithHeader.
package redisstream
