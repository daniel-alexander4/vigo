package nails

import (
	"context"
	"testing"
	"time"
)

func TestTimeCollector(t *testing.T) {
	c := &TimeCollector{}
	if c.Name() != "time" {
		t.Fatalf("Name() = %q, want %q", c.Name(), "time")
	}

	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	now := time.Now()

	hour, ok := result["hour"].(int)
	if !ok {
		t.Fatal("hour is not int")
	}
	if hour < 0 || hour > 23 {
		t.Errorf("hour = %d, want 0-23", hour)
	}

	weekday, ok := result["weekday"].(string)
	if !ok {
		t.Fatal("weekday is not string")
	}
	validDays := map[string]bool{
		"sunday": true, "monday": true, "tuesday": true, "wednesday": true,
		"thursday": true, "friday": true, "saturday": true,
	}
	if !validDays[weekday] {
		t.Errorf("weekday = %q, not a valid day", weekday)
	}

	day, ok := result["day"].(int)
	if !ok {
		t.Fatal("day is not int")
	}
	if day < 1 || day > 31 {
		t.Errorf("day = %d, want 1-31", day)
	}

	month, ok := result["month"].(int)
	if !ok {
		t.Fatal("month is not int")
	}
	if month < 1 || month > 12 {
		t.Errorf("month = %d, want 1-12", month)
	}

	year, ok := result["year"].(int)
	if !ok {
		t.Fatal("year is not int")
	}
	if year != now.Year() {
		t.Errorf("year = %d, want %d", year, now.Year())
	}

	if _, ok := result["timezone"].(string); !ok {
		t.Fatal("timezone is not string")
	}

	utcHour, ok := result["utc_hour"].(int)
	if !ok {
		t.Fatal("utc_hour is not int")
	}
	if utcHour < 0 || utcHour > 23 {
		t.Errorf("utc_hour = %d, want 0-23", utcHour)
	}
}
