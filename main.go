package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/extrame/xls"

	_ "github.com/lib/pq"
)

// ---------------------- CONFIG ----------------------

var (
	dbUser     = os.Getenv("POSTGRES_USER")
	dbPassword = os.Getenv("POSTGRES_PASSWORD")
	dbHost     = os.Getenv("POSTGRES_HOST")
	dbPort     = os.Getenv("POSTGRES_PORT")
	dbName     = os.Getenv("POSTGRES_DB")
)

// publicationSuffix returns the "MM_YY" suffix for the most recent quarterly
// INDEC publication. Publication months are 03, 06, 09, 12.
func publicationSuffix() string {
	now := time.Now()
	month := int(now.Month())
	year := now.Year()

	pubMonths := []int{12, 9, 6, 3}
	for _, m := range pubMonths {
		if month >= m {
			return fmt.Sprintf("%02d_%02d", m, year%100)
		}
	}
	// month < 3: use December of previous year
	return fmt.Sprintf("12_%02d", (year-1)%100)
}

func buildURLs() (string, string) {
	suffix := publicationSuffix()
	base := "https://www.indec.gob.ar/ftp/cuadros/economia"
	url1 := fmt.Sprintf("%s/sh_oferta_demanda_%s.xls", base, suffix)
	url2 := fmt.Sprintf("%s/sh_oferta_demanda_desest_%s.xls", base, suffix)
	return url1, url2
}

// Sheets are located by name, never by position. INDEC has already changed the
// layout of these files once (splitting the code and the description into two
// columns); an index that silently points at the wrong cuadro would ingest real
// numbers under the wrong label, with no error to notice.
//
// These names are also the values stored in pbi_data.cuadro, so they must stay
// stable even if INDEC re-cases or re-spaces the sheet title.

// Horizontal sheets from file 1 (rows = categories, columns = quarters/years)
var horizontalSheetNames = []string{
	"cuadro 1",
	"cuadro 3",
	"cuadro 4",
	"cuadro 8",
	"cuadro 11",
	"cuadro 12",
}

// Vertical sheets from file 2 (rows = quarters)
var verticalSheetNames = []string{
	"desestacionalizado n",
	"desestacionalizado v",
}

// normalizeSheetName makes lookups tolerant of the cosmetic differences INDEC
// introduces between publications: "Cuadro 1", "cuadro  1 " and "CUADRO 1" all
// collapse to "cuadro 1". It deliberately does NOT do prefix matching, so
// "cuadro 1" never resolves to "cuadro 10".
func normalizeSheetName(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(name)), " ")
}

// findSheet resolves a sheet by name and reports the position it was found at,
// so the resolved mapping can be logged and audited.
func findSheet(wb *xls.WorkBook, name string) (*xls.WorkSheet, int, error) {
	want := normalizeSheetName(name)
	for i := 0; i < wb.NumSheets(); i++ {
		s := wb.GetSheet(i)
		if s == nil {
			continue
		}
		if normalizeSheetName(s.Name) == want {
			return s, i, nil
		}
	}

	var available []string
	for i := 0; i < wb.NumSheets(); i++ {
		if s := wb.GetSheet(i); s != nil {
			available = append(available, fmt.Sprintf("[%d] %q", i, s.Name))
		}
	}
	return nil, -1, fmt.Errorf("hoja %q no encontrada. Hojas disponibles: %s",
		name, strings.Join(available, ", "))
}

func databaseURL() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		dbUser, dbPassword, dbHost, dbPort, dbName)
}

// ---------------------- TYPES ----------------------

type Observation struct {
	Fecha      time.Time
	Frecuencia string // "trimestral" or "anual"
	Variable   string
	Cuadro     string
	Valor      float64
	// Codigo holds the SNA code from column 0 of the horizontal sheets
	// (e.g. "P7", "P3_S14_S15"). Nil when the source row has no code —
	// aggregate rows such as "Oferta Global" — and for vertical sheets.
	Codigo *string
}

// ---------------------- DOWNLOAD ----------------------

func downloadFile(url, dest string) error {
	client := &http.Client{Timeout: 120 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/vnd.ms-excel,*/*")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("descarga fallida: status %d", resp.StatusCode)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		return err
	}

	fmt.Printf("Descargado: %s (%.2f MB)\n", dest, float64(written)/1024/1024)
	return nil
}

// ---------------------- XLS HELPERS ----------------------

// safeRow recovers from panics in the extrame/xls library.
func safeRow(sheet *xls.WorkSheet, r int) (row *xls.Row, ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	row = sheet.Row(r)
	ok = row != nil
	return
}

func cellStr(row *xls.Row, col int) string {
	if col >= int(row.LastCol()) {
		return ""
	}
	return strings.TrimSpace(row.Col(col))
}

func cellFloat(row *xls.Row, col int) *float64 {
	val := cellStr(row, col)
	if val == "" || val == "--" || val == "///" || val == "…" || val == "s/d" ||
		strings.ToLower(val) == "n/a" {
		return nil
	}
	val = strings.ReplaceAll(val, ",", "")
	v, err := strconv.ParseFloat(val, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func quarterEndDate(year, quarter int) time.Time {
	switch quarter {
	case 1:
		return time.Date(year, 3, 31, 0, 0, 0, 0, time.UTC)
	case 2:
		return time.Date(year, 6, 30, 0, 0, 0, 0, time.UTC)
	case 3:
		return time.Date(year, 9, 30, 0, 0, 0, 0, time.UTC)
	case 4:
		return time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC)
	}
	return time.Time{}
}

func annualDate(year int) time.Time {
	return time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC)
}

// ---------------------- HORIZONTAL SHEET PARSER ----------------------

type yearBlock struct {
	Year    int
	ColQ1   int // 0-indexed
	ColQ2   int
	ColQ3   int
	ColQ4   int
	ColYear int
}

var yearRegex = regexp.MustCompile(`^(\d{4})`)

// detectYears scans header rows to find year positions.
// Layout: year at col C, then C+1=Q1, C+2=Q2, C+3=Q3, C+4=Q4, C+5=Total
// But from inspection: year IS at the Q1 column position.
// col1=2004(Q1), col2(Q2), col3(Q3), col4(Q4), col5(Total), col6(sep), col7=2005(Q1)...
func detectYears(sheet *xls.WorkSheet) ([]yearBlock, int) {
	for r := 0; r <= 8; r++ {
		row, ok := safeRow(sheet, r)
		if !ok {
			continue
		}

		var blocks []yearBlock
		lastCol := int(row.LastCol())
		for c := 0; c < lastCol; c++ {
			val := cellStr(row, c)
			m := yearRegex.FindStringSubmatch(val)
			if m != nil {
				y, _ := strconv.Atoi(m[1])
				if y >= 1990 && y <= 2050 {
					blocks = append(blocks, yearBlock{
						Year:    y,
						ColQ1:   c,
						ColQ2:   c + 1,
						ColQ3:   c + 2,
						ColQ4:   c + 3,
						ColYear: c + 4,
					})
				}
			}
		}
		if len(blocks) >= 3 {
			return blocks, r
		}
	}
	return nil, -1
}

func parseHorizontalSheet(sheet *xls.WorkSheet, cuadro string) ([]Observation, error) {
	years, yearRow := detectYears(sheet)
	if len(years) == 0 {
		return nil, fmt.Errorf("no se encontraron años en hoja %q", cuadro)
	}
	fmt.Printf("  Hoja %q: %d años (%d-%d), fila años: %d\n",
		cuadro, len(years), years[0].Year, years[len(years)-1].Year, yearRow)

	maxRow := int(sheet.MaxRow)
	var obs []Observation

	for r := yearRow + 2; r <= maxRow; r++ {
		row, ok := safeRow(sheet, r)
		if !ok {
			continue
		}

		// Column 0 holds the SNA code, column 1 the description. Some aggregate
		// rows ("Oferta Global", "Demanda Global", "Discrepancia estadística")
		// have a description but no code, so the code alone cannot gate the row.
		// Headers ("Código"), footnotes, "Nota:" and "Fuente:" all live in column
		// 0 with column 1 empty, so they are filtered by the description check.
		codigo := cellStr(row, 0)
		variable := cellStr(row, 1)
		if variable == "" {
			continue
		}

		var codigoPtr *string
		if codigo != "" {
			codigoPtr = &codigo
		}

		// Check if row has any numeric data
		hasData := false
		for _, yb := range years {
			for _, c := range []int{yb.ColQ1, yb.ColQ2, yb.ColQ3, yb.ColQ4, yb.ColYear} {
				if cellFloat(row, c) != nil {
					hasData = true
					break
				}
			}
			if hasData {
				break
			}
		}
		if !hasData {
			continue
		}

		for _, yb := range years {
			for q, c := range []int{yb.ColQ1, yb.ColQ2, yb.ColQ3, yb.ColQ4} {
				v := cellFloat(row, c)
				if v != nil {
					obs = append(obs, Observation{
						Fecha:      quarterEndDate(yb.Year, q+1),
						Frecuencia: "trimestral",
						Variable:   variable,
						Cuadro:     cuadro,
						Valor:      *v,
						Codigo:     codigoPtr,
					})
				}
			}
			// Annual total
			v := cellFloat(row, yb.ColYear)
			if v != nil {
				obs = append(obs, Observation{
					Fecha:      annualDate(yb.Year),
					Frecuencia: "anual",
					Variable:   variable,
					Cuadro:     cuadro,
					Valor:      *v,
					Codigo:     codigoPtr,
				})
			}
		}
	}

	fmt.Printf("  → %d observaciones\n", len(obs))
	return obs, nil
}

// ---------------------- VERTICAL SHEET PARSER ----------------------

func parseVerticalSheet(sheet *xls.WorkSheet, cuadro string) ([]Observation, error) {
	maxRow := int(sheet.MaxRow)

	// Find header row (contains "Trimestre")
	headerRow := -1
	for r := 0; r <= min(maxRow, 10); r++ {
		row, ok := safeRow(sheet, r)
		if !ok {
			continue
		}
		for c := 0; c < int(row.LastCol()); c++ {
			if strings.EqualFold(cellStr(row, c), "Trimestre") {
				headerRow = r
				break
			}
		}
		if headerRow >= 0 {
			break
		}
	}
	if headerRow < 0 {
		return nil, fmt.Errorf("no se encontró fila de encabezados en hoja %q", cuadro)
	}

	hRow, _ := safeRow(sheet, headerRow)

	// Identify columns: col 0 = Año, col 1 = Trimestre, cols 2+ = variables
	var varCols []struct {
		col  int
		name string
	}
	for c := 2; c < int(hRow.LastCol()); c++ {
		name := cellStr(hRow, c)
		if name != "" {
			varCols = append(varCols, struct {
				col  int
				name string
			}{c, name})
		}
	}

	fmt.Printf("  Hoja %q: %d variables, header fila %d\n", cuadro, len(varCols), headerRow)

	var obs []Observation
	currentYear := 0

	for r := headerRow + 1; r <= maxRow; r++ {
		row, ok := safeRow(sheet, r)
		if !ok {
			continue
		}

		// Year column: only filled on first quarter of each year
		yearStr := cellStr(row, 0)
		if yearStr != "" {
			y, err := strconv.Atoi(yearStr)
			if err != nil {
				// Try float (e.g. "2004.0")
				yf, err2 := strconv.ParseFloat(yearStr, 64)
				if err2 != nil {
					continue
				}
				y = int(yf)
			}
			currentYear = y
		}
		if currentYear == 0 {
			continue
		}

		quarterStr := strings.ToUpper(strings.TrimSpace(cellStr(row, 1)))
		var quarter int
		switch quarterStr {
		case "I", "1":
			quarter = 1
		case "II", "2":
			quarter = 2
		case "III", "3":
			quarter = 3
		case "IV", "4":
			quarter = 4
		default:
			continue
		}

		fecha := quarterEndDate(currentYear, quarter)

		for _, vc := range varCols {
			v := cellFloat(row, vc.col)
			if v != nil {
				obs = append(obs, Observation{
					Fecha:      fecha,
					Frecuencia: "trimestral",
					Variable:   vc.name,
					Cuadro:     cuadro,
					Valor:      *v,
				})
			}
		}
	}

	fmt.Printf("  → %d observaciones\n", len(obs))
	return obs, nil
}

// ---------------------- SANITY CHECK ----------------------

// maxCountDrop is how far the observation count may fall below what the table
// already holds before the run is treated as suspect.
const maxCountDrop = 0.10

// checkCountDrop refuses to write when far fewer observations were parsed than
// the table already contains. The only previous guard was len(allObs) == 0,
// which a partially parsed file sails through: pointing the tool at the wrong
// file yields 1,062 observations instead of 9,655, and under -truncate that
// replaces good data with incomplete data reporting success.
//
// Growth is never suspect: every INDEC publication adds a quarter to the series.
func checkCountDrop(db *sql.DB, parsed int, force bool) error {
	var current int
	if err := db.QueryRow("SELECT count(*) FROM pbi_data").Scan(&current); err != nil {
		return fmt.Errorf("error contando filas actuales: %v", err)
	}
	return evaluateCountDrop(parsed, current, force)
}

// evaluateCountDrop holds the decision itself, split from the query so it can be
// exercised without a database.
func evaluateCountDrop(parsed, current int, force bool) error {
	if current == 0 {
		fmt.Println("Chequeo de caída: tabla vacía, no hay con qué comparar.")
		return nil
	}

	if parsed >= current {
		fmt.Printf("Chequeo de caída: %d observaciones contra %d en la tabla. OK.\n", parsed, current)
		return nil
	}

	drop := float64(current-parsed) / float64(current)
	if drop <= maxCountDrop {
		fmt.Printf("Chequeo de caída: %d contra %d en la tabla (-%.1f%%). Dentro del margen.\n",
			parsed, current, drop*100)
		return nil
	}

	if force {
		fmt.Printf("ADVERTENCIA: %d observaciones contra %d en la tabla (-%.1f%%). Continúa por -force.\n",
			parsed, current, drop*100)
		return nil
	}

	return fmt.Errorf("caída del %.1f%%: se parsearon %d observaciones y la tabla tiene %d "+
		"(máximo tolerado: %.0f%%). Puede indicar un cambio de formato en el archivo de INDEC. "+
		"Revisar el parseo y, si la caída es legítima, repetir con -force",
		drop*100, parsed, current, maxCountDrop*100)
}

// ---------------------- DATABASE INSERT ----------------------

func insertCopy(db *sql.DB, observations []Observation, truncateFirst bool) error {
	if truncateFirst {
		fmt.Println("Truncando tabla pbi_data...")
		if _, err := db.Exec("TRUNCATE TABLE pbi_data RESTART IDENTITY"); err != nil {
			return fmt.Errorf("error truncando: %v", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`COPY pbi_data (fecha, frecuencia, variable, cuadro, valor, codigo) FROM STDIN`)
	if err != nil {
		return fmt.Errorf("error preparando COPY: %v", err)
	}

	for i, o := range observations {
		_, err := stmt.Exec(o.Fecha.Format("2006-01-02"), o.Frecuencia, o.Variable, o.Cuadro, o.Valor, o.Codigo)
		if err != nil {
			return fmt.Errorf("error en COPY fila %d: %v", i, err)
		}
		if (i+1)%5000 == 0 {
			fmt.Printf("  Insertadas %d/%d filas...\n", i+1, len(observations))
		}
	}

	if err := stmt.Close(); err != nil {
		return fmt.Errorf("error cerrando COPY: %v", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error en commit: %v", err)
	}

	return nil
}

func insertUpsert(db *sql.DB, observations []Observation, truncateFirst bool) error {
	if truncateFirst {
		fmt.Println("Truncando tabla pbi_data...")
		if _, err := db.Exec("TRUNCATE TABLE pbi_data RESTART IDENTITY"); err != nil {
			return fmt.Errorf("error truncando: %v", err)
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Bulk-load into a staging table first, then merge in a single statement.
	// One INSERT per observation costs a network round trip each: ~6 minutes for
	// ~9,600 rows against a remote database, versus roughly a second this way.
	// The conflict check is not what was slow — it is an index lookup either
	// way; the per-statement protocol overhead was.
	//
	// ON COMMIT DROP ties the staging table to this transaction, so a failed run
	// leaves nothing behind. "ord" preserves the input order for deduplication.
	if _, err := tx.Exec(`
		CREATE TEMP TABLE pbi_data_stage (
			ord        BIGINT,
			fecha      DATE,
			frecuencia TEXT,
			variable   TEXT,
			cuadro     TEXT,
			valor      DOUBLE PRECISION,
			codigo     TEXT
		) ON COMMIT DROP
	`); err != nil {
		return fmt.Errorf("error creando tabla de staging: %v", err)
	}

	stmt, err := tx.Prepare(`COPY pbi_data_stage (ord, fecha, frecuencia, variable, cuadro, valor, codigo) FROM STDIN`)
	if err != nil {
		return fmt.Errorf("error preparando COPY a staging: %v", err)
	}

	for i, o := range observations {
		_, err := stmt.Exec(i, o.Fecha.Format("2006-01-02"), o.Frecuencia, o.Variable, o.Cuadro, o.Valor, o.Codigo)
		if err != nil {
			return fmt.Errorf("error en COPY a staging, fila %d: %v", i, err)
		}
	}

	if err := stmt.Close(); err != nil {
		return fmt.Errorf("error cerrando COPY a staging: %v", err)
	}

	// DISTINCT ON keeps the last occurrence of each key, matching the semantics
	// of the previous row-by-row upsert. Without it a repeated key would abort
	// the whole statement: ON CONFLICT DO UPDATE cannot touch the same row twice.
	var duplicados int
	if err := tx.QueryRow(`
		SELECT count(*) - count(DISTINCT (fecha, frecuencia, variable, cuadro))
		FROM pbi_data_stage
	`).Scan(&duplicados); err != nil {
		return fmt.Errorf("error contando duplicados en staging: %v", err)
	}
	if duplicados > 0 {
		fmt.Printf("  ADVERTENCIA: %d observaciones con clave repetida; se conserva la última de cada una.\n", duplicados)
	}

	res, err := tx.Exec(`
		INSERT INTO pbi_data (fecha, frecuencia, variable, cuadro, valor, codigo)
		SELECT DISTINCT ON (fecha, frecuencia, variable, cuadro)
		       fecha, frecuencia, variable, cuadro, valor, codigo
		FROM pbi_data_stage
		ORDER BY fecha, frecuencia, variable, cuadro, ord DESC
		ON CONFLICT (fecha, frecuencia, variable, cuadro)
		DO UPDATE SET
			valor = EXCLUDED.valor,
			codigo = EXCLUDED.codigo,
			ingested_at = NOW()
	`)
	if err != nil {
		return fmt.Errorf("error en merge desde staging: %v", err)
	}

	if n, err := res.RowsAffected(); err == nil {
		fmt.Printf("  Merge: %d filas insertadas o actualizadas.\n", n)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("error en commit: %v", err)
	}

	return nil
}

// ---------------------- MAIN ----------------------

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `
Uso: pib_downloader [opciones]

Descarga e ingesta los datos de PBI de INDEC en PostgreSQL.

Opciones:
  -file1 string
        Ruta al archivo XLS de oferta y demanda. Si se omite, descarga de INDEC.
  -file2 string
        Ruta al archivo XLS desestacionalizado. Si se omite, descarga de INDEC.
  -truncate
        Trunca la tabla antes de insertar (carga completa). Default: false.
  -upsert
        Usa INSERT ... ON CONFLICT (upsert) en vez de COPY. Default: false.
  -force
        Ignora el chequeo de caída de observaciones. Default: false.
        El proceso aborta antes de escribir si se parsean mas de un 10%% menos
        de observaciones que las que ya tiene la tabla.

Variables de entorno requeridas:
  POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_HOST, POSTGRES_PORT, POSTGRES_DB

Ejemplos:
  # Carga inicial completa
  pib_downloader -truncate

  # Re-ingesta incremental
  pib_downloader -upsert

  # Desde archivos locales
  pib_downloader -file1 ./oferta_demanda.xls -file2 ./desest.xls -upsert
`)
	}

	var (
		file1    string
		file2    string
		truncate bool
		upsert   bool
		force    bool
	)

	flag.StringVar(&file1, "file1", "", "Ruta a archivo XLS de oferta y demanda")
	flag.StringVar(&file2, "file2", "", "Ruta a archivo XLS desestacionalizado")
	flag.BoolVar(&truncate, "truncate", false, "Truncar tabla antes de insertar")
	flag.BoolVar(&upsert, "upsert", false, "Usar upsert (ON CONFLICT) en vez de COPY")
	flag.BoolVar(&force, "force", false, "Ignorar el chequeo de caída de observaciones")
	flag.Parse()

	// Validate DB config
	if dbUser == "" || dbPassword == "" || dbHost == "" || dbPort == "" || dbName == "" {
		log.Fatal("Faltan variables de entorno: POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_HOST, POSTGRES_PORT, POSTGRES_DB")
	}

	// Build dynamic URLs based on current date
	urlOfertaDemanda, urlDesest := buildURLs()
	fmt.Printf("Sufijo publicación: %s\n", publicationSuffix())
	fmt.Printf("  URL oferta/demanda: %s\n", urlOfertaDemanda)
	fmt.Printf("  URL desestacionalizado: %s\n", urlDesest)

	// Download or use local files
	if file1 == "" {
		file1 = filepath.Join(os.TempDir(), "sh_oferta_demanda.xls")
		fmt.Println("Descargando archivo de oferta y demanda...")
		if err := downloadFile(urlOfertaDemanda, file1); err != nil {
			log.Fatalf("Error descargando oferta y demanda: %v", err)
		}
	} else {
		if _, err := os.Stat(file1); os.IsNotExist(err) {
			log.Fatalf("Archivo no encontrado: %s", file1)
		}
		fmt.Printf("Usando archivo local: %s\n", file1)
	}

	if file2 == "" {
		file2 = filepath.Join(os.TempDir(), "sh_oferta_demanda_desest.xls")
		fmt.Println("Descargando archivo desestacionalizado...")
		if err := downloadFile(urlDesest, file2); err != nil {
			log.Fatalf("Error descargando desestacionalizado: %v", err)
		}
	} else {
		if _, err := os.Stat(file2); os.IsNotExist(err) {
			log.Fatalf("Archivo no encontrado: %s", file2)
		}
		fmt.Printf("Usando archivo local: %s\n", file2)
	}

	start := time.Now()

	// --- Parse file 1: horizontal sheets ---
	fmt.Printf("\nAbriendo %s...\n", file1)
	wb1, err := xls.Open(file1, "utf-8")
	if err != nil {
		log.Fatalf("Error abriendo archivo 1: %v", err)
	}

	fmt.Printf("Hojas: %d\n", wb1.NumSheets())
	for i := 0; i < wb1.NumSheets(); i++ {
		s := wb1.GetSheet(i)
		if s != nil {
			fmt.Printf("  [%d] %q\n", i, s.Name)
		}
	}

	var allObs []Observation

	// A missing or unparseable sheet aborts the run. Continuing would produce a
	// partial ingest that looks successful, which is how the previous format
	// change went unnoticed.
	for _, name := range horizontalSheetNames {
		sheet, idx, err := findSheet(wb1, name)
		if err != nil {
			log.Fatalf("Error resolviendo hoja: %v", err)
		}
		fmt.Printf("  %q → hoja [%d] %q\n", name, idx, sheet.Name)

		obs, err := parseHorizontalSheet(sheet, name)
		if err != nil {
			log.Fatalf("Error en hoja %q: %v", name, err)
		}
		allObs = append(allObs, obs...)
	}

	// --- Parse file 2: vertical sheets ---
	fmt.Printf("\nAbriendo %s...\n", file2)
	wb2, err := xls.Open(file2, "utf-8")
	if err != nil {
		log.Fatalf("Error abriendo archivo 2: %v", err)
	}

	fmt.Printf("Hojas: %d\n", wb2.NumSheets())
	for i := 0; i < wb2.NumSheets(); i++ {
		s := wb2.GetSheet(i)
		if s != nil {
			fmt.Printf("  [%d] %q\n", i, s.Name)
		}
	}

	for _, name := range verticalSheetNames {
		sheet, idx, err := findSheet(wb2, name)
		if err != nil {
			log.Fatalf("Error resolviendo hoja: %v", err)
		}
		fmt.Printf("  %q → hoja [%d] %q\n", name, idx, sheet.Name)

		obs, err := parseVerticalSheet(sheet, name)
		if err != nil {
			log.Fatalf("Error en hoja %q: %v", name, err)
		}
		allObs = append(allObs, obs...)
	}

	fmt.Printf("\nTotal observaciones: %d\n", len(allObs))

	if len(allObs) == 0 {
		log.Fatal("No se extrajeron observaciones. Verificar estructura de archivos.")
	}

	// --- Insert into database ---
	db, err := sql.Open("postgres", databaseURL())
	if err != nil {
		log.Fatalf("Error conectando a DB: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("Error ping DB: %v", err)
	}
	fmt.Println("Conectado a PostgreSQL.")

	// Runs before any write, so an aborted check leaves the table untouched —
	// including under -truncate, where the delete happens first.
	if err := checkCountDrop(db, len(allObs), force); err != nil {
		log.Fatalf("Error: %v", err)
	}

	if upsert {
		fmt.Println("Modo: UPSERT (ON CONFLICT)")
		if err := insertUpsert(db, allObs, truncate); err != nil {
			log.Fatalf("Error: %v", err)
		}
	} else {
		fmt.Println("Modo: COPY (bulk insert)")
		if err := insertCopy(db, allObs, truncate); err != nil {
			log.Fatalf("Error: %v", err)
		}
	}

	elapsed := time.Since(start)
	fmt.Printf("\nInsertadas: %d observaciones\n", len(allObs))
	fmt.Printf("Tiempo total: %s\n", elapsed.Round(time.Millisecond))
}
