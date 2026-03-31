# PBI Downloader

Descarga e ingesta los datos de PBI (Producto Bruto Interno) publicados por INDEC en una base de datos PostgreSQL.

## Fuentes de datos

### Archivo 1: `sh_oferta_demanda_03_26.xls`

Hojas con layout horizontal (filas = categorías, columnas = trimestres/años):

| Hoja | Observaciones | Descripción |
|------|--------------|-------------|
| cuadro 1 | 1,060 | Oferta y demanda globales, millones de pesos a precios de 2004 |
| cuadro 3 | 898 | Oferta y demanda globales, % del PIB a precios constantes |
| cuadro 4 | 2,310 | PIB por categoría de tabulación, millones de pesos a precios de 2004 |
| cuadro 8 | 1,055 | Oferta y demanda globales, millones de pesos a precios corrientes |
| cuadro 11 | 899 | Oferta y demanda globales, % del PIB a precios corrientes |
| cuadro 12 | 2,310 | PIB por categoría de tabulación, millones de pesos a precios corrientes |

### Archivo 2: `sh_oferta_demanda_desest_03_26.xls`

Hojas con layout vertical (filas = trimestres):

| Hoja | Observaciones | Descripción |
|------|--------------|-------------|
| desestacionalizado n | 528 | Valores desestacionalizados |
| desestacionalizado v | 522 | Variaciones desestacionalizadas |

**Total: ~9,582 observaciones**

## Requisitos

- Go 1.22+
- PostgreSQL

## Configuración

Variables de entorno requeridas:

```bash
export POSTGRES_USER=...
export POSTGRES_PASSWORD=...
export POSTGRES_HOST=...
export POSTGRES_PORT=...
export POSTGRES_DB=...
```

## Instalación

```bash
go build -o pib_downloader .
```

## Crear la tabla

```bash
psql "postgres://$POSTGRES_USER:$POSTGRES_PASSWORD@$POSTGRES_HOST:$POSTGRES_PORT/$POSTGRES_DB" -f schema.sql
```

## Uso

```bash
# Carga inicial completa (descarga de INDEC + truncate + COPY)
./pib_downloader -truncate

# Re-ingesta incremental (descarga de INDEC + upsert)
./pib_downloader -upsert

# Desde archivos locales
./pib_downloader -file1 ./oferta_demanda.xls -file2 ./desest.xls -upsert
```

### Opciones

| Flag | Descripción |
|------|-------------|
| `-file1` | Ruta al archivo XLS de oferta y demanda. Si se omite, descarga de INDEC. |
| `-file2` | Ruta al archivo XLS desestacionalizado. Si se omite, descarga de INDEC. |
| `-truncate` | Trunca la tabla antes de insertar (carga completa). |
| `-upsert` | Usa `INSERT ... ON CONFLICT` en vez de `COPY`. Seguro para re-ingestas. |

## Esquema de datos

Cada fila en `pbi_data` es una observación:

| Columna | Tipo | Descripción |
|---------|------|-------------|
| `fecha` | DATE | Fin del período (Q1: 03-31, Q2: 06-30, Q3: 09-30, Q4/anual: 12-31) |
| `frecuencia` | TEXT | `"trimestral"` o `"anual"` |
| `variable` | TEXT | Categoría económica (ej: "Producto Interno Bruto") |
| `cuadro` | TEXT | Hoja de origen (identifica precios constantes/corrientes, %, desest) |
| `valor` | DOUBLE PRECISION | Valor numérico |
| `ingested_at` | TIMESTAMPTZ | Timestamp de ingesta |

## Consultas de ejemplo

```sql
-- Resumen por cuadro y frecuencia
SELECT cuadro, frecuencia, count(*) FROM pbi_data GROUP BY 1, 2 ORDER BY 1, 2;

-- PIB trimestral a precios constantes
SELECT fecha, valor FROM pbi_data
WHERE variable = 'Producto Interno Bruto' AND cuadro = 'cuadro 1' AND frecuencia = 'trimestral'
ORDER BY fecha;

-- PIB desestacionalizado
SELECT fecha, valor FROM pbi_data
WHERE variable = 'PIB' AND cuadro = 'desestacionalizado n'
ORDER BY fecha;
```
