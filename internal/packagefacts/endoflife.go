package packagefacts

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Support is what endoflife.date says about a package's product.
type Support struct {
	// Tracked is false when endoflife.date names no product for the package.
	Tracked bool    `json:"tracked"`
	Product string  `json:"product,omitempty"`
	Cycles  []Cycle `json:"cycles,omitempty"`
}

// Cycle is one release cycle (Django's 4.2, React's 18) and when its
// support ends. Ends is zero when endoflife.date gives no date; Ended says
// support has already ended.
type Cycle struct {
	Name  string    `json:"name"`
	Ends  time.Time `json:"ends,omitzero"`
	Ended bool      `json:"ended,omitempty"`
}

// Support reads endoflife.date's product for the package: the purl
// identifiers map a package to a product, whose releases are its cycles.
func (c Client) Support(ctx context.Context, p Package) (Support, error) {
	api := base(c.EndOfLife, "https://endoflife.date")
	var ids struct {
		Result []struct {
			Identifier string `json:"identifier"`
			Product    struct {
				Name string `json:"name"`
			} `json:"product"`
		} `json:"result"`
	}
	if found, err := c.get(ctx, api+"/api/v1/identifiers/purl/", &ids); err != nil || !found {
		return Support{}, fmt.Errorf("endoflife.date: %v", orMissing(err))
	}
	want := map[string]bool{strings.ToLower(p.purl()): true}
	if p.Type == Go {
		want[strings.ToLower(Package{Type: Go, Name: goBasePath(p.Name)}.purl())] = true
	}
	product := ""
	for _, id := range ids.Result {
		purl, _, _ := strings.Cut(id.Identifier, "?")
		purl, _, _ = strings.Cut(purl, "@")
		if want[strings.ToLower(purl)] {
			product = id.Product.Name
			break
		}
	}
	if product == "" {
		return Support{}, nil
	}
	var doc struct {
		Result struct {
			Releases []struct {
				Name    string  `json:"name"`
				IsEol   bool    `json:"isEol"`
				EolFrom *string `json:"eolFrom"`
			} `json:"releases"`
		} `json:"result"`
	}
	if found, err := c.get(ctx, api+"/api/v1/products/"+product+"/", &doc); err != nil || !found {
		return Support{}, fmt.Errorf("endoflife.date product %s: %v", product, orMissing(err))
	}
	out := Support{Tracked: true, Product: product}
	for _, r := range doc.Result.Releases {
		cycle := Cycle{Name: r.Name, Ended: r.IsEol}
		if r.EolFrom != nil {
			t, err := time.Parse(time.DateOnly, *r.EolFrom)
			if err != nil {
				return Support{}, fmt.Errorf("endoflife.date product %s cycle %s: %w", product, r.Name, err)
			}
			cycle.Ends = t
		}
		out.Cycles = append(out.Cycles, cycle)
	}
	return out, nil
}

func orMissing(err error) error {
	if err == nil {
		return fmt.Errorf("not found")
	}
	return err
}
