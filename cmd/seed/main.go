// Command seed fills the database with a realistic worldwide demo fleet:
// every city of 15k+ people in the US, Japan, and Europe gets cars in
// proportion to its population (roughly Turo's one active car per thousand
// people), scattered around the city center but never in water.
//
// Data sources, fetched on first run and cached in cmd/seed/data/:
//   - GeoNames cities15000 (CC-BY 4.0) for city centers and populations
//   - Natural Earth 10m land and lakes polygons for the water mask
//
// The water test is exact against the polygons without being O(edges) per
// point: each city center pays one full ray cast, then every scattered point
// reuses that parity, flipped by crossings of the short center-to-point
// segment against locally indexed coastline edges.
//
// Usage:
//
//	DATABASE_URL=... go run ./cmd/seed -wipe -per-thousand 1.0
package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	citiesURL = "https://download.geonames.org/export/dump/cities15000.zip"
	landURL   = "https://raw.githubusercontent.com/martynafford/natural-earth-geojson/master/10m/physical/ne_10m_land.json"
	lakesURL  = "https://raw.githubusercontent.com/martynafford/natural-earth-geojson/master/10m/physical/ne_10m_lakes.json"
)

// europe is the EU, EEA, UK, Switzerland, and the western Balkans.
var europe = strings.Fields(`GB IE FR DE ES PT IT NL BE LU CH AT DK SE NO FI IS
	PL CZ SK HU RO BG GR HR SI RS BA MK AL ME EE LV LT MT CY`)

// fleet is every model the demo knows, with a photo slug the frontend ships
// and the regions it plausibly appears in.
type modelSpec struct {
	model     string
	year      int
	baseCents int
	regions   string // any of "us", "eu", "jp"
}

var fleet = []modelSpec{
	{"Toyota Corolla", 2022, 1200, "us eu jp"},
	{"Honda Civic", 2021, 1200, "us jp"},
	{"Tesla Model 3", 2023, 2400, "us eu"},
	{"Ford Mustang", 2020, 2600, "us"},
	{"Mazda MX-5", 2019, 2000, "us eu jp"},
	{"Subaru Outback", 2022, 1600, "us"},
	{"Hyundai Kona", 2023, 1300, "us eu"},
	{"VW Golf", 2018, 1100, "eu"},
	{"Kia Soul", 2021, 1000, "us"},
	{"Chevy Bolt", 2023, 1400, "us"},
	{"Mini Cooper", 2019, 1500, "us eu"},
	{"Jeep Wrangler", 2020, 2200, "us"},
	{"Toyota Prius", 2022, 1300, "us eu jp"},
	{"Fiat 500", 2019, 900, "eu"},
	{"Renault Clio", 2021, 1000, "eu"},
	{"Honda N-Box", 2022, 800, "jp"},
}

type city struct {
	name    string
	country string
	lat     float64
	lng     float64
	pop     int
}

func main() {
	perThousand := flag.Float64("per-thousand", 1.0, "cars per 1000 city residents")
	wipe := flag.Bool("wipe", false, "delete every existing car and reservation first")
	ownerCount := flag.Int("owners", 2000, "host accounts to spread the fleet across")
	flag.Parse()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fatal("set DATABASE_URL")
	}
	ctx := context.Background()

	cities := loadCities()
	mask := loadWaterMask()
	fmt.Printf("%d cities, %d coastline edges indexed\n", len(cities), mask.edgeCount)

	pool, err := pgxpool.New(ctx, databaseURL)
	must(err)
	defer pool.Close()

	if *wipe {
		_, err = pool.Exec(ctx, `TRUNCATE cars.reservations, cars.recurrences`)
		must(err)
		_, err = pool.Exec(ctx, `DELETE FROM cars.cars`)
		must(err)
		fmt.Println("wiped cars and reservations")
	}

	owners := seedOwners(ctx, pool, *ownerCount)
	random := rand.New(rand.NewSource(1010))

	rows := make([][]any, 0, 1<<20)
	skippedWater := 0
	for _, place := range cities {
		region := regionOf(place.country)
		pool := regionalFleet(region)
		count := int(float64(place.pop) / 1000 * *perThousand)
		if count == 0 {
			continue
		}
		// Scatter radius grows with the city, one to twelve km.
		sigmaKm := math.Min(12, math.Max(1, 1.2*math.Sqrt(float64(place.pop)/20000)))
		centerInWater := mask.inWater(place.lng, place.lat)
		for i := 0; i < count; i++ {
			lat, lng, ok := samplePoint(random, place, sigmaKm, mask, centerInWater)
			if !ok {
				skippedWater++
				continue
			}
			spec := pool[random.Intn(len(pool))]
			price := spec.baseCents + random.Intn(7)*100 - 300
			rows = append(rows, []any{
				owners[random.Intn(len(owners))],
				spec.model,
				spec.year - random.Intn(3),
				pgtype.Point{P: pgtype.Vec2{X: lng, Y: lat}, Valid: true},
				price,
			})
		}
	}

	fmt.Printf("placing %d cars (%d samples given up as water)\n", len(rows), skippedWater)
	copied, err := pool.CopyFrom(ctx, pgx.Identifier{"cars", "cars"},
		[]string{"owner_id", "model", "model_year", "location", "price_per_hour"}, pgx.CopyFromRows(rows))
	must(err)
	_, err = pool.Exec(ctx, `ANALYZE cars.cars`)
	must(err)
	fmt.Printf("seeded %d cars\n", copied)
}

// samplePoint scatters around the city center and rejects water, retrying a
// few times before giving up on that car.
func samplePoint(random *rand.Rand, place city, sigmaKm float64, mask *waterMask, centerInWater bool) (float64, float64, bool) {
	for attempt := 0; attempt < 12; attempt++ {
		dLatKm := random.NormFloat64() * sigmaKm / 2
		dLngKm := random.NormFloat64() * sigmaKm / 2
		lat := place.lat + dLatKm/110.57
		lng := place.lng + dLngKm/(111.32*math.Cos(place.lat*math.Pi/180))
		if mask.inWaterFrom(place.lng, place.lat, centerInWater, lng, lat) {
			continue
		}
		return lat, lng, true
	}
	if centerInWater {
		return 0, 0, false
	}
	// Fall back to (near) the center, which we know is dry.
	return place.lat, place.lng, true
}

func regionOf(country string) string {
	switch {
	case country == "US":
		return "us"
	case country == "JP":
		return "jp"
	default:
		return "eu"
	}
}

func regionalFleet(region string) []modelSpec {
	var pool []modelSpec
	for _, spec := range fleet {
		if strings.Contains(spec.regions, region) {
			pool = append(pool, spec)
		}
	}
	return pool
}

func seedOwners(ctx context.Context, pool *pgxpool.Pool, count int) []string {
	rows, err := pool.Query(ctx, `
		WITH created AS (
			INSERT INTO cars.users (email, display_name)
			SELECT NULL, 'Host ' || g
			FROM generate_series(1, $1) g
			WHERE NOT EXISTS (SELECT 1 FROM cars.users WHERE display_name = 'Host 1')
			RETURNING id
		)
		SELECT id FROM created
		UNION ALL
		SELECT id FROM cars.users WHERE display_name LIKE 'Host %'`, count)
	must(err)
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		must(rows.Scan(&id))
		ids = append(ids, id)
	}
	must(rows.Err())
	if len(ids) == 0 {
		fatal("no owners")
	}
	return ids
}

// --- cities ---

func loadCities() []city {
	path := cached("cities15000.zip", citiesURL)
	reader, err := zip.OpenReader(path)
	must(err)
	defer func() { _ = reader.Close() }()
	file, err := reader.File[0].Open()
	must(err)
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(file)
	must(err)

	wanted := map[string]bool{"US": true, "JP": true}
	for _, code := range europe {
		wanted[code] = true
	}

	var cities []city
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) < 15 || !wanted[fields[8]] {
			continue
		}
		lat, latErr := strconv.ParseFloat(fields[4], 64)
		lng, lngErr := strconv.ParseFloat(fields[5], 64)
		pop, popErr := strconv.Atoi(fields[14])
		if latErr != nil || lngErr != nil || popErr != nil || pop < 15000 {
			continue
		}
		cities = append(cities, city{name: fields[1], country: fields[8], lat: lat, lng: lng, pop: pop})
	}
	return cities
}

// --- water mask ---

type edge struct{ x1, y1, x2, y2 float64 }

// waterMask holds land and lake rings. A point is in water when it is outside
// every land ring or inside a lake ring.
type waterMask struct {
	landRings [][]point
	lakeRings [][]point
	// grid buckets edges by half-degree cell for the segment test.
	grid      map[int64][]edge
	edgeCount int
}

type point struct{ x, y float64 }

func loadWaterMask() *waterMask {
	mask := &waterMask{grid: map[int64][]edge{}}
	mask.landRings = loadRings(cached("ne_10m_land.json", landURL))
	mask.lakeRings = loadRings(cached("ne_10m_lakes.json", lakesURL))
	for _, ring := range append(append([][]point{}, mask.landRings...), mask.lakeRings...) {
		for i := 1; i < len(ring); i++ {
			mask.addEdge(edge{ring[i-1].x, ring[i-1].y, ring[i].x, ring[i].y})
		}
	}
	return mask
}

func (mask *waterMask) addEdge(candidate edge) {
	mask.edgeCount++
	minCellX := int(math.Floor(math.Min(candidate.x1, candidate.x2) * 2))
	maxCellX := int(math.Floor(math.Max(candidate.x1, candidate.x2) * 2))
	minCellY := int(math.Floor(math.Min(candidate.y1, candidate.y2) * 2))
	maxCellY := int(math.Floor(math.Max(candidate.y1, candidate.y2) * 2))
	for cellX := minCellX; cellX <= maxCellX; cellX++ {
		for cellY := minCellY; cellY <= maxCellY; cellY++ {
			key := int64(cellX)<<32 | int64(uint32(cellY))
			mask.grid[key] = append(mask.grid[key], candidate)
		}
	}
}

// inWater pays the full ray cast: once per city center.
func (mask *waterMask) inWater(x, y float64) bool {
	if !insideAny(mask.landRings, x, y) {
		return true
	}
	return insideAny(mask.lakeRings, x, y)
}

// inWaterFrom answers for a scattered point by flipping the center's parity
// once per coastline crossing of the short center-to-point segment.
func (mask *waterMask) inWaterFrom(centerX, centerY float64, centerInWater bool, x, y float64) bool {
	crossings := 0
	minCellX := int(math.Floor(math.Min(centerX, x) * 2))
	maxCellX := int(math.Floor(math.Max(centerX, x) * 2))
	minCellY := int(math.Floor(math.Min(centerY, y) * 2))
	maxCellY := int(math.Floor(math.Max(centerY, y) * 2))
	seen := map[edge]bool{}
	for cellX := minCellX; cellX <= maxCellX; cellX++ {
		for cellY := minCellY; cellY <= maxCellY; cellY++ {
			for _, candidate := range mask.grid[int64(cellX)<<32|int64(uint32(cellY))] {
				if seen[candidate] {
					continue
				}
				seen[candidate] = true
				if segmentsCross(centerX, centerY, x, y, candidate) {
					crossings++
				}
			}
		}
	}
	if crossings%2 == 1 {
		return !centerInWater
	}
	return centerInWater
}

func insideAny(rings [][]point, x, y float64) bool {
	inside := false
	for _, ring := range rings {
		for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
			if (ring[i].y > y) != (ring[j].y > y) &&
				x < (ring[j].x-ring[i].x)*(y-ring[i].y)/(ring[j].y-ring[i].y)+ring[i].x {
				inside = !inside
			}
		}
	}
	return inside
}

func segmentsCross(ax, ay, bx, by float64, candidate edge) bool {
	d1 := cross(candidate.x1, candidate.y1, candidate.x2, candidate.y2, ax, ay)
	d2 := cross(candidate.x1, candidate.y1, candidate.x2, candidate.y2, bx, by)
	d3 := cross(ax, ay, bx, by, candidate.x1, candidate.y1)
	d4 := cross(ax, ay, bx, by, candidate.x2, candidate.y2)
	return ((d1 > 0) != (d2 > 0)) && ((d3 > 0) != (d4 > 0))
}

func cross(originX, originY, throughX, throughY, atX, atY float64) float64 {
	return (throughX-originX)*(atY-originY) - (throughY-originY)*(atX-originX)
}

func loadRings(path string) [][]point {
	raw, err := os.ReadFile(path)
	must(err)
	var parsed struct {
		Features []struct {
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	must(json.Unmarshal(raw, &parsed))
	var rings [][]point
	appendRing := func(coordinates [][]float64) {
		ring := make([]point, 0, len(coordinates))
		for _, pair := range coordinates {
			ring = append(ring, point{pair[0], pair[1]})
		}
		rings = append(rings, ring)
	}
	for _, feature := range parsed.Features {
		switch feature.Geometry.Type {
		case "Polygon":
			var polygon [][][]float64
			must(json.Unmarshal(feature.Geometry.Coordinates, &polygon))
			for _, ring := range polygon {
				appendRing(ring)
			}
		case "MultiPolygon":
			var multi [][][][]float64
			must(json.Unmarshal(feature.Geometry.Coordinates, &multi))
			for _, polygon := range multi {
				for _, ring := range polygon {
					appendRing(ring)
				}
			}
		}
	}
	return rings
}

// --- plumbing ---

func cached(name, url string) string {
	directory := filepath.Join("cmd", "seed", "data")
	must(os.MkdirAll(directory, 0o755))
	path := filepath.Join(directory, name)
	if _, err := os.Stat(path); err == nil {
		return path
	}
	fmt.Printf("downloading %s\n", url)
	response, err := http.Get(url)
	must(err)
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		fatal(fmt.Sprintf("%s: HTTP %d", url, response.StatusCode))
	}
	file, err := os.Create(path)
	must(err)
	_, err = io.Copy(file, response.Body)
	must(err)
	must(file.Close())
	return path
}

func must(err error) {
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
