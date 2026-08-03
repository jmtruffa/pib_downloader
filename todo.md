# TODO

## 1. Completar la cobertura de tests

Ya existen (`main_test.go`): `evaluateCountDrop` y `normalizeSheetName`.

**Falta**

- Fixture `.xls` chico (2-3 años, 4-5 filas por hoja) commiteado en `testdata/`, con los dos
  layouts: horizontal con columna de código, y vertical.
- `parseHorizontalSheet`: nombre desde la columna 1, código desde la 0, filas sin código
  incluidas con `Codigo == nil`, notas al pie descartadas.
- `findSheet` sobre un workbook real: que resuelva por nombre y que falle listando las hojas
  disponibles. Hoy solo está testeada la normalización, no la búsqueda.
- `publicationSuffix`: la tabla de casos que está en el README (enero → diciembre del año
  anterior, etc.). Usa `time.Now()` directo, así que hay que inyectar la fecha para poder
  testearlo.
- Identidad contable como test de integración: en `cuadro 1`, PIB + Importaciones = Oferta Global
  = Demanda Global para cada trimestre. Es el chequeo que detecta un parseo torcido sin depender
  de valores hardcodeados que cambian con cada publicación.
