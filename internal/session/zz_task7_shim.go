package session

// TEMPORARY (Task 7): the window's bridge needs the Discover intent before
// Task 5 lands. Task 5 defines the real one, with its handler, in msgs.go;
// this file is deleted when Task 5 merges.

// Discover starts a discovery station from a phrase or a link.
type Discover struct{ Query string }

func (Discover) isMsg() {}
