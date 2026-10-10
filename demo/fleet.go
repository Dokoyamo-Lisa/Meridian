package main

// The demo's world: servers that do not exist, people who are made up, and the plans and monitors
// a small service would have. Every address is from the ranges kept for documentation (RFC 5737,
// RFC 3849), so nothing here points at anyone's machine.

// machine is one demo server and the host its agent pretends to run on.
type machine struct {
	Name      string
	City, CC  string
	Lat, Lon  float64
	IPv4      string
	IPv6      string
	CPU       string
	Cores     int
	MemGB     float64
	DiskGB    float64
	OS        string
	Kernel    string
	Arch      string
	Virt      string
	Temps     bool // a dedicated machine with sensors
	Protocols []string
	Price     float64
	Cycle     string // month | year
	BwTB      float64
	Note      string
	UTC       float64 // its people's time zone, hours from UTC: when evenings are
	Busy      float64 // how much people like it, besides their home server
	CPUBase   float64 // per cent at rest
	MemBase   float64 // share of memory in use at rest
	DiskBase  float64 // share of the disk in use
}

var fleet = []machine{
	{Name: "Tokyo", City: "Tokyo", CC: "JP", Lat: 35.6762, Lon: 139.6503, IPv4: "203.0.113.10", IPv6: "2001:db8:10::10",
		CPU: "AMD EPYC 7B13", Cores: 2, MemGB: 2, DiskGB: 40, OS: "Ubuntu 24.04.3 LTS", Kernel: "6.8.0-85-generic",
		Arch: "amd64", Virt: "kvm", Protocols: []string{"vless", "hysteria2", "shadowsocks"}, Price: 6, Cycle: "month",
		BwTB: 2, UTC: 9, Busy: 1.3, CPUBase: 3, MemBase: 0.41, DiskBase: 0.36},
	{Name: "Singapore", City: "Singapore", CC: "SG", Lat: 1.3521, Lon: 103.8198, IPv4: "203.0.113.24",
		CPU: "Intel Xeon Platinum 8259CL", Cores: 2, MemGB: 4, DiskGB: 50, OS: "Debian GNU/Linux 12 (bookworm)",
		Kernel: "6.1.0-40-cloud-amd64", Arch: "amd64", Virt: "kvm", Protocols: []string{"vless", "hysteria2"}, Price: 5,
		Cycle: "month", BwTB: 1, UTC: 8, Busy: 1.0, CPUBase: 2.5, MemBase: 0.24, DiskBase: 0.29},
	{Name: "Sydney", City: "Sydney", CC: "AU", Lat: -33.8688, Lon: 151.2093, IPv4: "203.0.113.77",
		CPU: "Ampere Altra", Cores: 2, MemGB: 4, DiskGB: 40, OS: "Ubuntu 24.04.3 LTS", Kernel: "6.8.0-85-generic",
		Arch: "arm64", Virt: "kvm", Protocols: []string{"vless", "hysteria2"}, Price: 4, Cycle: "month", UTC: 11,
		Busy: 0.5, CPUBase: 1.5, MemBase: 0.22, DiskBase: 0.31},
	{Name: "Frankfurt", City: "Frankfurt am Main", CC: "DE", Lat: 50.1109, Lon: 8.6821, IPv4: "203.0.113.35",
		IPv6: "2001:db8:35::1", CPU: "AMD Ryzen 9 7950X3D 16-Core Processor", Cores: 16, MemGB: 64, DiskGB: 960,
		OS: "Debian GNU/Linux 13 (trixie)", Kernel: "6.12.48+deb13-amd64", Arch: "amd64", Virt: "none", Temps: true,
		Protocols: []string{"vless", "vmess", "shadowsocks", "wireguard"}, Price: 49, Cycle: "month", BwTB: 20,
		Note: "Dedicated machine, also runs the office's backups", UTC: 2, Busy: 1.6, CPUBase: 4, MemBase: 0.18, DiskBase: 0.47},
	{Name: "London", City: "London", CC: "GB", Lat: 51.5072, Lon: -0.1276, IPv4: "203.0.113.41",
		CPU: "Intel Xeon E5-2680 v4", Cores: 2, MemGB: 2, DiskGB: 30, OS: "Ubuntu 22.04.5 LTS", Kernel: "5.15.0-153-generic",
		Arch: "amd64", Virt: "xen", Protocols: []string{"vless", "hysteria2"}, Price: 60, Cycle: "year", UTC: 1,
		Busy: 0.8, CPUBase: 2, MemBase: 0.38, DiskBase: 0.52},
	{Name: "New York", City: "New York", CC: "US", Lat: 40.7128, Lon: -74.006, IPv4: "203.0.113.52",
		CPU: "AMD EPYC 7763 64-Core Processor", Cores: 2, MemGB: 4, DiskGB: 60, OS: "Ubuntu 24.04.3 LTS",
		Kernel: "6.8.0-85-generic", Arch: "amd64", Virt: "kvm", Protocols: []string{"vless", "shadowsocks", "wireguard"},
		Price: 7, Cycle: "month", BwTB: 3, UTC: -4, Busy: 1.1, CPUBase: 2.5, MemBase: 0.27, DiskBase: 0.33},
	{Name: "Los Angeles", City: "Los Angeles", CC: "US", Lat: 34.0522, Lon: -118.2437, IPv4: "203.0.113.66",
		CPU: "Intel Xeon Gold 6338 CPU @ 2.00GHz", Cores: 4, MemGB: 4, DiskGB: 80, OS: "Rocky Linux 9.6 (Blue Onyx)",
		Kernel: "5.14.0-570.42.2.el9_6.x86_64", Arch: "amd64", Virt: "kvm", Protocols: []string{"vless", "hysteria2"},
		Price: 8, Cycle: "month", BwTB: 4, UTC: -7, Busy: 0.9, CPUBase: 2, MemBase: 0.3, DiskBase: 0.26},
	{Name: "São Paulo", City: "São Paulo", CC: "BR", Lat: -23.5505, Lon: -46.6333, IPv4: "203.0.113.88",
		CPU: "Intel Xeon Platinum 8370C CPU @ 2.80GHz", Cores: 1, MemGB: 1, DiskGB: 20, OS: "Alpine Linux v3.22",
		Kernel: "6.12.45-0-virt", Arch: "amd64", Virt: "lxc", Protocols: []string{"vless", "shadowsocks"}, Price: 3,
		Cycle: "month", UTC: -3, Busy: 0.4, CPUBase: 3.5, MemBase: 0.46, DiskBase: 0.41},
}

// hub is where the panel is drawn on the status page's globe: a place, not where it runs.
var hub = map[string]any{"city": "Amsterdam", "cc": "NL", "lat": 52.3676, "lon": 4.9041}

type plan struct {
	Name     string
	Note     string
	QuotaGB  float64
	Devices  int
	Refuse   bool // devices over the limit are turned away
	Price    float64
	Duration int // months; 0 = no end
}

var plans = []plan{
	{Name: "Starter", Note: "Phones and a laptop", QuotaGB: 50, Devices: 2, Price: 3},
	{Name: "Standard", Note: "Most people", QuotaGB: 200, Devices: 4, Price: 6},
	{Name: "Unlimited", Note: "Families and heavy users", Devices: 6, Refuse: true, Price: 12},
}

// person is one demo user and how they use the service.
type person struct {
	Name     string
	Username string
	Note     string
	Plan     string  // a plan's name; "" = their own settings below
	QuotaGB  float64 // without a plan
	Reset    string  // without a plan: month (on the 1st), 30d (every 30 days from the start) or none
	Devices  int     // the most they have online at once
	Limit    int     // without a plan: the device limit
	Started  float64 // days ago
	Expires  float64 // days from now; 0 = never, negative = already over
	DailyGB  float64 // what they use on an average day
	Home     string  // the server they use most
	Servers  []string
	Paused   bool
}

var people = []person{
	{Name: "Ava Brooks", Username: "ava", Note: "Design team", Plan: "Standard", Devices: 3, Started: 41, DailyGB: 5.5, Home: "Tokyo"},
	{Name: "Ben Carter", Username: "ben", Note: "Design team", Plan: "Starter", Devices: 2, Started: 12, DailyGB: 5.1, Home: "Singapore"},
	{Name: "Chloe Martin", Username: "chloe", Note: "Family plan - four people", Plan: "Unlimited", Devices: 5, Started: 64, DailyGB: 14, Home: "Frankfurt"},
	{Name: "Daniel Weber", Username: "daniel", Note: "Pays per 30 days", QuotaGB: 100, Reset: "30d", Devices: 2, Limit: 3, Started: 26, DailyGB: 3.6, Home: "Frankfurt"},
	{Name: "Emma Fischer", Username: "emma", Plan: "Starter", Devices: 2, Started: 33, DailyGB: 1.2, Home: "London"},
	{Name: "Felix Laurent", Username: "felix", Note: "Travels a lot", Plan: "Standard", Devices: 3, Started: 19, DailyGB: 2.5, Home: "London"},
	{Name: "Grace Miller", Username: "grace", Note: "Renews by bank transfer", Plan: "Standard", Devices: 2, Started: 87, Expires: 3, DailyGB: 2.1, Home: "New York"},
	{Name: "Hugo Silva", Username: "hugo", Plan: "Starter", Devices: 2, Started: 15, DailyGB: 1.5, Home: "São Paulo", Servers: []string{"São Paulo", "New York"}},
	{Name: "Iris Novak", Username: "iris", Note: "Lab - no limits", Reset: "none", Devices: 4, Started: 18, DailyGB: 8, Home: "Frankfurt"},
	{Name: "Jack Wilson", Username: "jack", Note: "Prepaid 30 GB", QuotaGB: 30, Reset: "none", Devices: 1, Limit: 1, Started: 40, Expires: -2, DailyGB: 1.0, Home: "Los Angeles"},
	{Name: "Kate O'Neill", Username: "kate", Note: "Paused while away", Plan: "Standard", Devices: 2, Started: 58, DailyGB: 3, Home: "London", Paused: true},
	{Name: "Leo Rossi", Username: "leo", Plan: "Standard", Devices: 2, Started: 9, DailyGB: 3, Home: "London"},
	{Name: "Mia Johansson", Username: "mia", Note: "Shares with a tablet", Plan: "Starter", Devices: 3, Started: 30, DailyGB: 0.8, Home: "Frankfurt"},
	{Name: "Noah Schmidt", Username: "noah", Plan: "Unlimited", Devices: 3, Started: 52, DailyGB: 11, Home: "New York"},
	{Name: "Olivia Park", Username: "olivia", Note: "Remote office", Plan: "Standard", Devices: 2, Started: 7, DailyGB: 4.2, Home: "Los Angeles"},
	{Name: "Sam Taylor", Username: "sam", Plan: "Starter", Devices: 1, Started: 21, DailyGB: 0.9, Home: "Sydney"},
}

// monitor is a ping monitor; where the target is decides how far each server is from it.
type monitor struct {
	Name, Target, Kind string
	Port               int
	Every              int
	Public             bool
	Anycast            bool // answered nearby everywhere
	Lat, Lon           float64
}

var monitors = []monitor{
	{Name: "Cloudflare DNS", Target: "1.1.1.1", Kind: "icmp", Every: 60, Public: true, Anycast: true},
	{Name: "GitHub", Target: "github.com", Kind: "tcp", Port: 443, Every: 60, Public: true, Lat: 38.95, Lon: -77.45},
	{Name: "Office router", Target: "office.example.com", Kind: "icmp", Every: 300, Lat: 52.37, Lon: 4.9},
}

// destinations people's devices visit, most visited first.
var destinations = []struct {
	Host string
	Port int
	Net  string
}{
	{"www.youtube.com", 443, "tcp"}, {"www.google.com", 443, "tcp"}, {"github.com", 443, "tcp"},
	{"www.wikipedia.org", 443, "tcp"}, {"www.netflix.com", 443, "tcp"}, {"open.spotify.com", 443, "tcp"},
	{"discord.com", 443, "tcp"}, {"www.reddit.com", 443, "tcp"}, {"api.openai.com", 443, "tcp"},
	{"www.youtube.com", 443, "udp"}, {"slack.com", 443, "tcp"}, {"zoom.us", 443, "tcp"},
	{"www.apple.com", 443, "tcp"}, {"login.microsoftonline.com", 443, "tcp"}, {"stackoverflow.com", 443, "tcp"},
	{"time.cloudflare.com", 123, "udp"},
}
