package wow

import "fmt"

func oneEdit(n int64, err error) error {
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("edit affected %d records, expected one", n)
	}
	return nil
}
