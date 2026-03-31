-- Tabla para datos del PBI (Producto Bruto Interno) de INDEC
-- Fuentes: sh_oferta_demanda y sh_oferta_demanda_desest

CREATE TABLE IF NOT EXISTS pbi_data (
    id          SERIAL PRIMARY KEY,
    fecha       DATE             NOT NULL,   -- Fin del período (Q1→03-31, Q2→06-30, Q3→09-30, Q4/anual→12-31)
    frecuencia  TEXT             NOT NULL,   -- "trimestral" o "anual"
    variable    TEXT             NOT NULL,   -- Nombre de la categoría (ej: "PIB", "Importaciones")
    cuadro      TEXT             NOT NULL,   -- Hoja de origen (identifica precios constantes/corrientes, %, desest)
    valor       DOUBLE PRECISION NOT NULL,
    ingested_at TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

-- Índices para consultas típicas
CREATE INDEX IF NOT EXISTS idx_pbi_fecha ON pbi_data (fecha);
CREATE INDEX IF NOT EXISTS idx_pbi_variable ON pbi_data (variable);
CREATE INDEX IF NOT EXISTS idx_pbi_cuadro ON pbi_data (cuadro);
CREATE INDEX IF NOT EXISTS idx_pbi_fecha_variable ON pbi_data (fecha, variable);

-- Unique constraint para upsert
CREATE UNIQUE INDEX IF NOT EXISTS idx_pbi_unique
    ON pbi_data (fecha, frecuencia, variable, cuadro);
