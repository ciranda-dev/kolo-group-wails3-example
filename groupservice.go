package main

// RoomSummary is one line of the room list.
type RoomSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GroupService is the Wails service the window calls.
type GroupService struct{ cfg Config }

// NewGroupService returns the service for cfg.
func NewGroupService(cfg Config) *GroupService { return &GroupService{cfg: cfg} }

// Version returns the release the app reports.
func (s *GroupService) Version() string { return version }

// Rooms returns the rooms of this device; it is always empty until rooms are built.
func (s *GroupService) Rooms() []RoomSummary { return []RoomSummary{} }
