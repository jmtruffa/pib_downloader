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

const (
	urlOfertaDemanda = "https://www.indec.gob.ar/ftp/cuadros/economia/sh_oferta_demanda_03_26.xls"
	urlDesest        = "https://www.indec.gob.ar/ftp/cuadros/economia/sh_oferta_demanda_desest_03_26.xls"
)

// Horizontal sheets from file 1 (indexed by sheet position)
var horizontalSheets = []struct {
	index int
	name  string
}{
	{1, "cuadro 1"},
	{3, "cuadro 3"},
	{4, "cuadro 4"},
	{8, "cuadro 8"},
	{11, "cuadro 11"},
	{12, "cuadro 12"},
}

// Vertical sheets from file 2
var verticalSheetDefs = []struct {
	index  int
	cuadro string
}{
	{0, "desestacionalizado n"},
	{1, "desestacionalizado v"},
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

		variable := cellStr(row, 0)
		if variable == "" {
			continue
		}
		// Stop at footnotes
		if strings.HasPrefix(variable, "(") || strings.HasPrefix(variable, "Nota") ||
			strings.HasPrefix(variable, "Fuente") {
			continue
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

	stmt, err := tx.Prepare(`COPY pbi_data (fecha, frecuencia, variable, cuadro, valor) FROM STDIN`)
	if err != nil {
		return fmt.Errorf("error preparando COPY: %v", err)
	}

	for i, o := range observations {
		_, err := stmt.Exec(o.Fecha.Format("2006-01-02"), o.Frecuencia, o.Variable, o.Cuadro, o.Valor)
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

	stmt, err := tx.Prepare(`
		INSERT INTO pbi_data (fecha, frecuencia, variable, cuadro, valor)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (fecha, frecuencia, variable, cuadro)
		DO UPDATE SET
			valor = EXCLUDED.valor,
			ingested_at = NOW()
	`)
	if err != nil {
		return fmt.Errorf("error preparando upsert: %v", err)
	}
	defer stmt.Close()

	for i, o := range observations {
		_, err := stmt.Exec(o.Fecha.Format("2006-01-02"), o.Frecuencia, o.Variable, o.Cuadro, o.Valor)
		if err != nil {
			return fmt.Errorf("error upsert fila %d: %v", i, err)
		}
		if (i+1)%5000 == 0 {
			fmt.Printf("  Upserted %d/%d filas...\n", i+1, len(observations))
		}
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
	)

	flag.StringVar(&file1, "file1", "", "Ruta a archivo XLS de oferta y demanda")
	flag.StringVar(&file2, "file2", "", "Ruta a archivo XLS desestacionalizado")
	flag.BoolVar(&truncate, "truncate", false, "Truncar tabla antes de insertar")
	flag.BoolVar(&upsert, "upsert", false, "Usar upsert (ON CONFLICT) en vez de COPY")
	flag.Parse()

	// Validate DB config
	if dbUser == "" || dbPassword == "" || dbHost == "" || dbPort == "" || dbName == "" {
		log.Fatal("Faltan variables de entorno: POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_HOST, POSTGRES_PORT, POSTGRES_DB")
	}

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

	for _, hs := range horizontalSheets {
		sheet := wb1.GetSheet(hs.index)
		if sheet == nil {
			fmt.Printf("ADVERTENCIA: hoja índice %d no encontrada, saltando %q.\n", hs.index, hs.name)
			continue
		}

		obs, err := parseHorizontalSheet(sheet, hs.name)
		if err != nil {
			fmt.Printf("ERROR en hoja %q: %v\n", hs.name, err)
			continue
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

	for _, vs := range verticalSheetDefs {
		sheet := wb2.GetSheet(vs.index)
		if sheet == nil {
			fmt.Printf("ADVERTENCIA: hoja índice %d no encontrada, saltando %q.\n", vs.index, vs.cuadro)
			continue
		}

		obs, err := parseVerticalSheet(sheet, vs.cuadro)
		if err != nil {
			fmt.Printf("ERROR en hoja %q: %v\n", vs.cuadro, err)
			continue
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
