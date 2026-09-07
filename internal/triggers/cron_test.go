package triggers

import (
	"strings"
	"testing"
	"time"
)

func TestCronSupportsNamesListsRangesAndSteps(t *testing.T) {
	schedule, err := ParseCron("5/10 9-17/2 * JAN,MARCH MON-FRI", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, time.January, 5, 8, 0, 0, 0, time.UTC) // Monday
	if got, want := schedule.Next(after), time.Date(2026, time.January, 5, 9, 5, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
	if got, want := schedule.Next(time.Date(2026, time.January, 5, 9, 5, 0, 0, time.UTC)), time.Date(2026, time.January, 5, 9, 15, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("stepped next = %s, want %s", got, want)
	}
}

func TestCronMinuteUnitStepRunsThroughFieldMaximum(t *testing.T) {
	schedule, err := ParseCron("5/1 * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, time.September, 7, 10, 4, 0, 0, time.UTC)
	for minute := 5; minute <= 59; minute++ {
		want := time.Date(2026, time.September, 7, 10, minute, 0, 0, time.UTC)
		got := schedule.Next(after)
		if !got.Equal(want) {
			t.Fatalf("next after %s = %s, want %s", after, got, want)
		}
		after = got
	}
	if got, want := schedule.Next(after), time.Date(2026, time.September, 7, 11, 5, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next hour = %s, want %s", got, want)
	}
}

func TestCronDistinguishesSingleValuesFromUnitSteps(t *testing.T) {
	after := time.Date(2026, time.September, 7, 10, 5, 0, 0, time.UTC) // Monday
	tests := []struct {
		name       string
		expression string
		want       time.Time
	}{
		{"plain minute", "5 * * * *", time.Date(2026, time.September, 7, 11, 5, 0, 0, time.UTC)},
		{"stepped minute", "5/1 * * * *", time.Date(2026, time.September, 7, 10, 6, 0, 0, time.UTC)},
		{"plain hour", "0 5 * * *", time.Date(2026, time.September, 8, 5, 0, 0, 0, time.UTC)},
		{"stepped hour", "0 5/1 * * *", time.Date(2026, time.September, 7, 11, 0, 0, 0, time.UTC)},
		{"plain day", "0 0 5 * *", time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC)},
		{"stepped day", "0 0 5/1 * *", time.Date(2026, time.September, 8, 0, 0, 0, 0, time.UTC)},
		{"plain month", "0 0 1 3 *", time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC)},
		{"stepped month", "0 0 1 3/1 *", time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)},
		{"plain named month", "0 0 1 MAR *", time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC)},
		{"stepped named month", "0 0 1 MAR/1 *", time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)},
		{"plain weekday", "0 0 * * 1", time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC)},
		{"stepped weekday", "0 0 * * 1/1", time.Date(2026, time.September, 8, 0, 0, 0, 0, time.UTC)},
		{"plain named weekday", "0 0 * * MON", time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC)},
		{"stepped named weekday", "0 0 * * MON/1", time.Date(2026, time.September, 8, 0, 0, 0, 0, time.UTC)},
		{"wildcard unit step", "*/1 * * * *", time.Date(2026, time.September, 7, 10, 6, 0, 0, time.UTC)},
		{"single value range unit step", "5-5/1 * * * *", time.Date(2026, time.September, 7, 11, 5, 0, 0, time.UTC)},
		{"range unit step", "4-5/1 * * * *", time.Date(2026, time.September, 7, 11, 4, 0, 0, time.UTC)},
		{"plain value before unit step in list", "5,10/1 * * * *", time.Date(2026, time.September, 7, 10, 10, 0, 0, time.UTC)},
		{"unit step before plain value in list", "5/1,10 * * * *", time.Date(2026, time.September, 7, 10, 6, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schedule, err := ParseCron(test.expression, "UTC")
			if err != nil {
				t.Fatal(err)
			}
			if got := schedule.Next(after); !got.Equal(test.want) {
				t.Fatalf("next = %s, want %s", got, test.want)
			}
		})
	}
}

func TestCronUsesVixieDayOfMonthWeekdayOR(t *testing.T) {
	schedule, err := ParseCron("0 0 13 * FRI", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := schedule.Next(time.Date(2024, time.June, 12, 0, 0, 0, 0, time.UTC)), time.Date(2024, time.June, 13, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("day-of-month next = %s, want %s", got, want)
	}
	if got, want := schedule.Next(time.Date(2024, time.June, 13, 0, 0, 0, 0, time.UTC)), time.Date(2024, time.June, 14, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("weekday next = %s, want %s", got, want)
	}
}

func TestCronDSTGapDoesNotFireAndRepeatFiresTwice(t *testing.T) {
	schedule, err := ParseCron("30 1 * * *", "Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	gapStart := time.Date(2025, time.March, 30, 0, 59, 0, 0, time.UTC)
	if got, want := schedule.Next(gapStart), time.Date(2025, time.March, 31, 0, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("after DST gap = %s, want %s", got, want)
	}

	fallStart := time.Date(2025, time.October, 26, 0, 0, 0, 0, time.UTC)
	first := schedule.Next(fallStart)
	second := schedule.Next(first)
	if want := time.Date(2025, time.October, 26, 0, 30, 0, 0, time.UTC); !first.Equal(want) {
		t.Fatalf("first repeated occurrence = %s, want %s", first, want)
	}
	if want := time.Date(2025, time.October, 26, 1, 30, 0, 0, time.UTC); !second.Equal(want) {
		t.Fatalf("second repeated occurrence = %s, want %s", second, want)
	}
}

func TestParseCronRejectsNonFiveFieldAndUnsafeForms(t *testing.T) {
	tests := []struct {
		expression string
		timezone   string
		want       string
	}{
		{"0 0 0 * * *", "UTC", "exactly five"},
		{"@daily", "UTC", "exactly five"},
		{"CRON_TZ=UTC 0 0 * * *", "UTC", "environment"},
		{"PATH=/bin 0 0 * * *", "UTC", "environment"},
		{"0 0 * * *\n", "UTC", "one line"},
		{"0 0 * * *", "Not/A_Zone", "timezone"},
		{"0 0 * * *", "", "required"},
		{"0 0 * * *", "Local", "IANA"},
		{"0 0 * * *", " UTC ", "IANA"},
		{"60 0 * * *", "UTC", "between 0 and 59"},
		{"*/0 0 * * *", "UTC", "positive"},
		{"5/0 * * * *", "UTC", "positive"},
		{"5/-1 * * * *", "UTC", "positive"},
		{"5/nope * * * *", "UTC", "positive"},
		{"5/ * * * *", "UTC", "invalid step"},
		{"/1 * * * *", "UTC", "invalid step"},
		{"5/1/1 * * * *", "UTC", "invalid step"},
		{"0 0 * DEC-JAN *", "UTC", "must not precede"},
		{"0 0 31 2 *", "UTC", "no possible occurrence"},
	}
	for _, test := range tests {
		t.Run(test.expression+test.timezone, func(t *testing.T) {
			_, err := ParseCron(test.expression, test.timezone)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseCronAllowsImpossibleDayOfMonthWhenRestrictedWeekdayCanMatch(t *testing.T) {
	schedule, err := ParseCron("0 0 31 2 MON", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := schedule.Next(time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)), time.Date(2026, time.February, 2, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %s, want %s", got, want)
	}
}
