package zstd

// ObserveHeld has held see the bytes each decode reserves while it holds
// a slot, and their release (negative).
func ObserveHeld(d *Decoder, held func(bytes int64)) { d.held = held }
