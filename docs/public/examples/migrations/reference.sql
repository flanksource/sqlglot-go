CREATE TABLE items (
  id INTEGER NOT NULL PRIMARY KEY,
  label TEXT NOT NULL,
  notes TEXT
);

INSERT INTO items (id, label) VALUES
  (1, 'Notebook'),
  (2, 'Pencil');
