package strategy

// SelectedSourceTarget は元手法の通知時点の返済前後量を固定します。
// 数量は元mini10枚を100とした正規化micro単位です。
type SelectedSourceTarget struct {
	SourceID                                   string
	BeforeMicro, QuantityMicro, RemainingMicro int
}

// SelectedSourceEvent は実約定に影響されない元通知です。
type SelectedSourceEvent struct {
	Signal                              Signal
	Parent, SourceID                    string
	QuantityMicro, ParentRemainingMicro int
	Targets                             []SelectedSourceTarget
}

// SelectedLot は当起動の一件の実約定と証券会社建玉IDを対応させます。
type SelectedLot struct {
	ID, HoldID, EntryID, Parent, SourceID, Owner, Kind string
	Campaign, Side, Quantity, InitialQuantity          int
	Entry, At, BestGain, Realized, Fees                float64
	Goal                                               int
	Reason                                             string
	GoalGroups                                         []SelectedCloseGoal
}

// SelectedCloseGoal は実玉へ残る返済希望を元通知群ごとに保持します。
type SelectedCloseGoal struct {
	GroupID, Reason string
	Quantity        int
}

// SelectedCloseGroup は一つの元返済通知に対応する実返済の完了と粗益を集計します。
type SelectedCloseGroup struct {
	Parent, Reason   string
	Expected, Filled int
	Gross            float64
	Qualified        bool
}

// SelectedIntent は政策から会計へ渡す発注希望です。
// 新規QuantityMicroは100基準、返済Quantityは実商品整数枚です。
type SelectedIntent struct {
	PolicyID, CancelIntentID                        string
	CloseGroupID                                    string
	Parent, SourceID, ActualID, Owner, Kind, Reason string
	Side, Campaign, QuantityMicro, Quantity         int
}

// SelectedFillEvent は実帳簿反映直後の逐次約定を政策へ通知します。
type SelectedFillEvent struct {
	ExecutionID string
	Intent      SelectedIntent
	LotID       string
	Quantity    int
	Gross, Time float64
}

// SelectedSnapshot は政策が参照する現在所有と予約です。外部保有を含みません。
type SelectedSnapshot struct {
	Cap                            int
	Lots                           []SelectedLot
	ReservedClose                  map[string]int
	PendingParents, ClosingParents map[string]bool
	SourceRemaining                map[string]int
	ParentRemaining                map[string]int
	SideAvailable                  map[int]int
}

// SelectedObservation は原行順で生成した追加指標です。未来足を含みません。
type SelectedObservation struct {
	DayBuy, DaySell SelectedDay164Support
	DayShort        SelectedDay164Short
	Night           selectedNightStateView
	NightAvailable  bool
	NightIndex      int64
	NightTime       float64
}

// SelectedPortfolio は固定元手法と実所有を分離した選定三手法の会計です。
type SelectedPortfolio struct {
	Version       int
	Shadow        *Portfolio
	ShadowPending []Signal
	SourceLots    map[string]*selectedSourceLot
	SourceOrder   []string
	Lots          []SelectedLot
	Intents       map[string]SelectedIntent
	CloseGroups   map[string]*SelectedCloseGroup
	Forced        string
	ForcedOwners  map[string]string
	Night         *selectedNightPolicy
	Day           *SelectedDay164Policy
	Last          Observation
}

type selectedSourceLot struct {
	SourceID, Parent, Owner        string
	Campaign, Side, RemainingMicro int
}

// selectedMethod は選定済み不変IDを内部短縮IDで識別します。
//
// 引数: method(string)。返り値: bool。
func selectedMethod(method string) bool {
	return method == "D164_15" || method == "P164_06" || method == "U165_03"
}
