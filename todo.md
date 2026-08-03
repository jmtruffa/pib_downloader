# TODO

## 1. Tests

**Por qué**

El parser no tiene ninguna verificación automática. Los dos bugs corregidos el 2026-08-03 —el
nombre de variable leído de la columna equivocada y la selección de hojas por índice— vivieron en
producción porque la ingesta terminaba con exit 0 y nadie miraba el conteo de filas.

**Qué testear**

- Fixture `.xls` chico (2-3 años, 4-5 filas por hoja) commiteado en `testdata/`, con los dos
  layouts: horizontal con columna de código, y vertical.
- `parseHorizontalSheet`: nombre desde la columna 1, código desde la 0, filas sin código
  incluidas con `Codigo == nil`, notas al pie descartadas.
- `findSheet`: match case-insensitive, y que `"cuadro 1"` NO resuelva a `"cuadro 10"`.
- `publicationSuffix`: la tabla de casos que está en el README (enero → diciembre del año
  anterior, etc.). Hoy usa `time.Now()` directo, así que hay que inyectar la fecha para poder
  testearlo.
- Identidad contable como test de integración: en `cuadro 1`, PIB + Importaciones = Oferta Global
  = Demanda Global para cada trimestre. Es el chequeo que detecta un parseo torcido sin depender
  de valores hardcodeados que cambian con cada publicación.

## 2. Chequeo de sanidad post-parseo

**Por qué**

El único guard actual es `len(allObs) == 0`. Una corrida que devuelva 1.062 observaciones en vez
de las ~9.655 esperadas pasa sin ruido y, con `-truncate`, reemplaza datos buenos por datos
incompletos.

**Opciones**

- Mínimo esperado por hoja.
- Comparar contra el `count(*)` actual de la tabla y abortar si la caída supera un umbral
  (ej. 10%), con un flag `-force` para saltearlo cuando el cambio sea legítimo.

## 3. Limpieza pendiente

- `DROP TABLE pbi_data_backup_20260803;` — backup de 17.033 filas tomado antes del `-truncate`
  del 2026-08-03. Borrar cuando la ingesta nueva esté confirmada en uso.
