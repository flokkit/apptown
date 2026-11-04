package secret

import "github.com/flokkit/apptown/internal/deploy/appcontract"

// DevDelivery gives local containers fresh generated values and declared defaults,
// scoped as deployment delivery is. It reads no bindings or stores and persists
// nothing. Credentials without either are withheld by name. A nil spec delivers
// nothing. The profile is default, else the first declared in name order.
func DevDelivery(spec *spec, c *appcontract.Contract) (Delivery, []string, error) {
	if spec == nil {
		return Delivery{}, nil, nil
	}
	if err := spec.CheckScopesAgainst(c); err != nil {
		return Delivery{}, nil, err
	}
	d := Delivery{Profile: SpecProfile(spec, appcontract.DefaultProfile), ByConsumer: map[string]map[string]string{}}
	var withheld []string
	for _, s := range spec.SecretsIn(d.Profile) {
		var value string
		switch {
		case s.Generate:
			var err error
			value, err = GenerateValue()
			if err != nil {
				return Delivery{}, nil, err
			}
		case s.HasDefault:
			value = s.Default
		default:
			withheld = append(withheld, s.Name)
			continue
		}
		consumers, all := spec.ConsumersOf(s.Name)
		if all {
			consumers = allConsumers(c)
		}
		for _, consumer := range consumers {
			if d.ByConsumer[consumer] == nil {
				d.ByConsumer[consumer] = map[string]string{}
			}
			d.ByConsumer[consumer][s.Name] = value
		}
	}
	return d, withheld, nil
}
