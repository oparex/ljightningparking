package parking

import "math"

const (
	zone1 = 1.7
	zone2 = 1.0
	zone3 = 0.7
)

type Zone struct {
	Name    string
	Price   float64
	MaxTime float64
}

func (z Zone) GetParkingFee(hours int64) float64 {
	return math.Min(float64(hours), z.MaxTime) * z.Price
}

var Zones = map[string]Zone{
	"C1":  {"C1", zone1, 4},
	"C4":  {"C4", zone1, 2},
	"C5":  {"C5", zone1, 2},
	"C6":  {"C6", zone1, 2},
	"C7":  {"C7", zone1, 2},
	"C9":  {"C9", zone1, 2},
	"C10": {"C10", zone1, 2},
	"C11": {"C11", zone1, 4},
	"C13": {"C13", zone1, 4},
	"C14": {"C14", zone1, 4},
	"B1":  {"B1", zone2, 6},
	"Pr":  {"Pr", zone2, 6},
	"Kr":  {"Kr", zone2, 6},
	"Mi":  {"Mi", zone2, 6},
	"B2":  {"B2", zone3, 10},
	"B3":  {"B3", zone3, 10},
	"J1":  {"J1", zone3, 10},
	"J2":  {"J2", zone3, 10},
	"J3":  {"J3", zone3, 10},
	"J4":  {"J4", zone3, 10},
	"ZAD": {"ZAD", zone3, 10},
	"ZAL": {"ZAL", zone3, 10},
	"POD": {"POD", zone3, 10},
	"KA":  {"KA", zone3, 10},
	"SO1": {"SO1", zone3, 10},
	"SO2": {"SO2", zone3, 10},
	"Vo1": {"Vo1", zone3, 10},
	"Mo1": {"Mo1", zone3, 10},
	"Mo2": {"Mo2", zone3, 10},
	"Ko1": {"Ko1", zone3, 10},
	"Po1": {"Po1", zone3, 10},
	"R1":  {"R1", zone3, 10},
	"R2":  {"R2", zone3, 10},
	"R3":  {"R3", zone3, 10},
	"Tr":  {"Tr", zone3, 10},
	"Rj":  {"Rj", zone3, 10},
	"RjJ": {"RjJ", zone3, 10},
	"Mu":  {"Mu", zone3, 10},
	"V1":  {"V1", zone3, 10},
	"V2":  {"V2", zone3, 10},
	"V3":  {"V3", zone3, 10},
	"V4":  {"V4", zone3, 10},
	"V5":  {"V5", zone3, 10},
	"V6":  {"V6", zone3, 10},
	"Rd1": {"Rd1", zone3, 10},
	"Rd2": {"Rd2", zone3, 10},
	"Si1": {"Si1", zone3, 10},
	"Si2": {"Si2", zone3, 10},
	"Si3": {"Si3", zone3, 10},
	"ST":  {"ST", zone3, 10},
	"RA":  {"RA", zone3, 10},
	"GA":  {"GA", zone3, 10},
	"ŠM":  {"ŠM", zone3, 10},
	"ŠT1": {"ŠT1", zone3, 10},
	"ŠT2": {"ŠT2", zone3, 10},
	"ŠT3": {"ŠT3", zone3, 10},
	"ŠT4": {"ŠT4", zone3, 10},
	"ŠT5": {"ŠT5", zone3, 10},
	"SA":  {"SA", zone3, 10},
	"ČR1": {"ČR1", zone3, 10},
	"ČR2": {"ČR2", zone3, 10},
	"ČR3": {"ČR3", zone3, 10},
	"ČR4": {"ČR4", zone3, 10},
	"ČR5": {"ČR5", zone3, 10},
	"RU":  {"RU", zone3, 10},
	"STO": {"STO", zone3, 10},
	"TO":  {"TO", zone3, 10},
	"DR":  {"DR", zone3, 10},
	"PO":  {"PO", zone3, 10},
	"GR":  {"GR", zone3, 10},
	"KO":  {"KO", zone3, 10},
}
