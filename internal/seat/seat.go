// Package seat manages seat state: AVAILABLE, LOCKED (ephemeral Redis), BOOKED (durable Mongo).
// TODO: implement GetSeatMap (merge Mongo booked + Redis locked), seat document CRUD.
package seat
