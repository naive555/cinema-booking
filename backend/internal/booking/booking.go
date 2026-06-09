// Package booking owns the seat-reservation flow: acquire lock → write Mongo → release lock.
// TODO: implement BookSeat, CancelBooking; enforce unique compound index; publish events to queue.
package booking
