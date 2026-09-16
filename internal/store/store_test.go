package store

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTestStore 建一个临时库，用固定时区避免依赖运行环境。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(Options{
		Path:     filepath.Join(dir, "test.db"),
		Location: time.FixedZone("CST", 8*3600),
		Cutoff:   4 * time.Hour,
	})
	if err != nil {
		t.Fatalf("打开测试库: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if err := s.EnsureSeed(); err != nil {
		t.Fatalf("初始化种子数据: %v", err)
	}
	return s
}

func TestEnsureSeed(t *testing.T) {
	s := newTestStore(t)

	children, err := s.ListChildren()
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("应有 1 个默认孩子，得到 %d", len(children))
	}
	if children[0].Name != DefaultChildName {
		t.Errorf("默认孩子名字 = %q, want %q", children[0].Name, DefaultChildName)
	}

	sem, err := s.CurrentSemester(children[0].ID)
	if err != nil {
		t.Fatalf("应有默认当前学期: %v", err)
	}
	if sem.Name != DefaultSemesterName {
		t.Errorf("默认学期名 = %q, want %q", sem.Name, DefaultSemesterName)
	}

	// 重复调用不应产生多余数据。
	if err := s.EnsureSeed(); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.ListChildren(); len(c) != 1 {
		t.Errorf("重复 EnsureSeed 后孩子数 = %d, want 1", len(c))
	}
}

func TestSemester_CurrentUnique(t *testing.T) {
	s := newTestStore(t)
	child, _ := s.ListChildren()
	cid := child[0].ID

	// 新建第二个学期并切为当前。
	sem2, err := s.CreateSemester(cid, "一年级下册", true)
	if err != nil {
		t.Fatal(err)
	}

	sems, _ := s.ListSemesters(cid)
	curCount := 0
	for _, m := range sems {
		if m.IsCurrent {
			curCount++
		}
	}
	if curCount != 1 {
		t.Errorf("当前学期数 = %d, want 1", curCount)
	}

	cur, _ := s.CurrentSemester(cid)
	if cur.ID != sem2 {
		t.Errorf("当前学期 = %d, want %d", cur.ID, sem2)
	}
}

func TestSetCurrentSemester_RejectsOtherChild(t *testing.T) {
	s := newTestStore(t)
	c1, _ := s.ListChildren()

	c2, err := s.CreateChild("二宝")
	if err != nil {
		t.Fatal(err)
	}
	sem2, err := s.CreateSemester(c2, "三年级上册", true)
	if err != nil {
		t.Fatal(err)
	}

	// 不能把别人的学期设为当前。
	if err := s.SetCurrentSemester(c1[0].ID, sem2); err == nil {
		t.Error("应拒绝跨孩子的学期切换")
	}
}

func TestImport_Idempotent(t *testing.T) {
	s := newTestStore(t)
	child, _ := s.ListChildren()
	cid := child[0].ID
	sem, _ := s.CurrentSemester(cid)

	req := ImportRequest{
		ChildID:    cid,
		SemesterID: sem.ID,
		SourceName: "第一课",
		Text:       "我们是中国人，我爱北京天安门。",
	}

	pv, err := s.PreviewImport(req)
	if err != nil {
		t.Fatal(err)
	}
	if pv.UniqueHanzi == 0 {
		t.Fatal("预览应抽出汉字")
	}
	if pv.ToInsert != pv.UniqueHanzi {
		t.Errorf("首次导入应全部新增: ToInsert=%d Unique=%d", pv.ToInsert, pv.UniqueHanzi)
	}

	if _, err := s.CommitImport(req); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM hanzi`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != pv.UniqueHanzi {
		t.Errorf("入库字数 = %d, want %d", count, pv.UniqueHanzi)
	}

	// ★ 关键：再次导入同一份文本，一个字都不应新增。
	pv2, err := s.PreviewImport(req)
	if err != nil {
		t.Fatal(err)
	}
	if pv2.ToInsert != 0 {
		t.Errorf("重复导入 ToInsert = %d, want 0", pv2.ToInsert)
	}
	if pv2.AlreadyExists != pv.UniqueHanzi {
		t.Errorf("重复导入 AlreadyExists = %d, want %d", pv2.AlreadyExists, pv.UniqueHanzi)
	}

	if _, err := s.CommitImport(req); err != nil {
		t.Fatal(err)
	}
	var count2 int
	s.DB().QueryRow(`SELECT COUNT(*) FROM hanzi`).Scan(&count2)
	if count2 != count {
		t.Errorf("重复导入后字数 = %d, want %d（幂等被破坏）", count2, count)
	}

	// 批次应记录两次，关联的 seen_count 累加。
	var batches int
	s.DB().QueryRow(`SELECT COUNT(*) FROM import_batch`).Scan(&batches)
	if batches != 2 {
		t.Errorf("批次数 = %d, want 2", batches)
	}
}

// 每个新导入的字都应有一条 review_state（due_on 为 NULL，走新字通道）。
func TestImport_CreatesReviewState(t *testing.T) {
	s := newTestStore(t)
	child, _ := s.ListChildren()
	cid := child[0].ID
	sem, _ := s.CurrentSemester(cid)

	req := ImportRequest{
		ChildID: cid, SemesterID: sem.ID, SourceName: "test", Text: "天地人",
	}
	pv, _ := s.PreviewImport(req)
	if _, err := s.CommitImport(req); err != nil {
		t.Fatal(err)
	}

	var withState int
	err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM hanzi h
		 JOIN review_state rs ON rs.hanzi_id = h.id
		 WHERE h.child_id = ? AND rs.due_on IS NULL`, cid).Scan(&withState)
	if err != nil {
		t.Fatal(err)
	}
	if withState != pv.UniqueHanzi {
		t.Errorf("有调度状态的字数 = %d, want %d", withState, pv.UniqueHanzi)
	}
}

// seq 应跨批次接续，保证「先导入的先学」。
func TestImport_SeqContinues(t *testing.T) {
	s := newTestStore(t)
	child, _ := s.ListChildren()
	cid := child[0].ID
	sem, _ := s.CurrentSemester(cid)

	if _, err := s.CommitImport(ImportRequest{
		ChildID: cid, SemesterID: sem.ID, SourceName: "a", Text: "天地人",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitImport(ImportRequest{
		ChildID: cid, SemesterID: sem.ID, SourceName: "b", Text: "日月星",
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.DB().Query(
		`SELECT ch, seq FROM hanzi WHERE child_id = ? ORDER BY seq`, cid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var seqs []int
	var chars []string
	for rows.Next() {
		var ch string
		var seq int
		rows.Scan(&ch, &seq)
		chars = append(chars, ch)
		seqs = append(seqs, seq)
	}

	// 第二批的字 seq 应全部大于第一批。
	if len(seqs) != 6 {
		t.Fatalf("应有 6 个字，得到 %d (%v)", len(seqs), chars)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Errorf("seq 未严格递增: %v", seqs)
			break
		}
	}
}

func TestImport_RejectsForeignSemester(t *testing.T) {
	s := newTestStore(t)
	c1, _ := s.ListChildren()
	c2, _ := s.CreateChild("二宝")
	sem2, _ := s.CreateSemester(c2, "三年级", true)

	_, err := s.CommitImport(ImportRequest{
		ChildID: c1[0].ID, SemesterID: sem2, SourceName: "x", Text: "天地人",
	})
	if err == nil {
		t.Error("应拒绝把字导入到别的孩子的学期")
	}
}

func TestImport_EmptyText(t *testing.T) {
	s := newTestStore(t)
	child, _ := s.ListChildren()
	cid := child[0].ID
	sem, _ := s.CurrentSemester(cid)

	_, err := s.CommitImport(ImportRequest{
		ChildID: cid, SemesterID: sem.ID, SourceName: "x", Text: "hello 123 !!!",
	})
	if err == nil {
		t.Error("没有汉字时应报错")
	}
}

// 并发导入同一份文本，不应产生重复字。
func TestImport_Concurrent(t *testing.T) {
	s := newTestStore(t)
	child, _ := s.ListChildren()
	cid := child[0].ID
	sem, _ := s.CurrentSemester(cid)

	req := ImportRequest{
		ChildID: cid, SemesterID: sem.ID, SourceName: "并发", Text: "天地人日月星",
	}

	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.CommitImport(req)
		}(i)
	}
	wg.Wait()

	// 只要没有写坏数据即可，个别失败可接受（并发下同一份文本互相竞争）。
	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM hanzi`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Errorf("并发导入后字数 = %d, want 6", count)
	}

	// 每个字只应有一条 review_state。
	var states int
	s.DB().QueryRow(`SELECT COUNT(*) FROM review_state WHERE child_id = ?`, cid).Scan(&states)
	if states != 6 {
		t.Errorf("review_state 行数 = %d, want 6", states)
	}
}

func TestSettings_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	child, _ := s.ListChildren()
	cid := child[0].ID

	got, err := s.GetSettings(cid)
	if err != nil {
		t.Fatal(err)
	}
	if got != DefaultSettings() {
		t.Errorf("默认设置 = %+v, want %+v", got, DefaultSettings())
	}

	want := Settings{DailyNewCap: 8, DailyReviewCap: 30, HeaviestThresh: 10, DayCutoffHour: 0}
	if err := s.SaveSettings(cid, want); err != nil {
		t.Fatal(err)
	}

	got2, err := s.GetSettings(cid)
	if err != nil {
		t.Fatal(err)
	}
	if got2 != want {
		t.Errorf("往返后设置 = %+v, want %+v", got2, want)
	}

	if d := got2.Cutoff(); d != 0 {
		t.Errorf("日切点 = %v, want 0", d)
	}
}

func TestDeleteBatch(t *testing.T) {
	s := newTestStore(t)
	child, _ := s.ListChildren()
	cid := child[0].ID
	sem, _ := s.CurrentSemester(cid)

	batchID, err := s.CommitImport(ImportRequest{
		ChildID: cid, SemesterID: sem.ID, SourceName: "a", Text: "天地人",
	})
	if err != nil {
		t.Fatal(err)
	}

	deleted, err := s.DeleteBatch(batchID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 3 {
		t.Errorf("删除了 %d 个字, want 3", deleted)
	}

	var count int
	s.DB().QueryRow(`SELECT COUNT(*) FROM hanzi WHERE child_id = ?`, cid).Scan(&count)
	if count != 0 {
		t.Errorf("删除批次后剩余 %d 个字, want 0", count)
	}
}

func TestMigrate_RejectsNewerVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	s, err := Open(Options{Path: path, Location: time.UTC, Cutoff: 0})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// 重新打开应拒绝，而不是静默用错结构。
	if _, err := Open(Options{Path: path, Location: time.UTC, Cutoff: 0}); err == nil {
		t.Error("应拒绝更高的结构版本")
	}
}
