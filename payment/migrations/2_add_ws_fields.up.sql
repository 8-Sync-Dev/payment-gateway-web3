ALTER TABLE transactions 
ADD COLUMN tx_id TEXT UNIQUE,
ADD COLUMN received_amount NUMERIC(24, 8);
