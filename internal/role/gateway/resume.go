package gateway

// Resume reverses Drain for one device, or for the whole mesh when name is empty.
// The next catalogue refresh routes to the device again. Only this Gateway's marks
// are cleared; other Gateways learn about the resumed device on their own.
func (g *Gateway) Resume(name string) {
	g.mu.Lock()
	if name == "" {
		g.meshDraining = false
	} else {
		delete(g.draining, name)
	}
	g.generation++
	views := []*view{}
	for v := range g.views {
		views = append(views, v)
	}
	g.mu.Unlock()
	for _, v := range views {
		go v.refresh()
	}
}
