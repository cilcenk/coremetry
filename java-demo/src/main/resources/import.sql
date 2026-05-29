-- ── Accounts ────────────────────────────────────────────────────────────────
-- 20 retail accounts. Account numbers are TR33 + 13 digits to look like IBAN
-- tails. IDs 1..20 line up with the %02d suffix the LoadGenerator builds.
-- One CREDIT account runs a negative balance on purpose; one account is FROZEN
-- so the 409 path always has a target.
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000001', 'Ada Lovelace',      'CHECKING',  4250.75, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000002', 'Grace Hopper',      'SAVINGS',  18900.00, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000003', 'Alan Turing',       'CHECKING',   320.40, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000004', 'Katherine Johnson', 'SAVINGS',  62000.00, 'USD', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000005', 'Linus Torvalds',    'CHECKING',  1500.00, 'EUR', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000006', 'Margaret Hamilton', 'CREDIT',    -820.00, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000007', 'Dennis Ritchie',    'CHECKING',  9870.10, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000008', 'Barbara Liskov',    'SAVINGS',  44120.55, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000009', 'Donald Knuth',      'CHECKING',    55.00, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000010', 'Edsger Dijkstra',   'CREDIT',     200.00, 'EUR', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000011', 'Tim Berners-Lee',   'CHECKING',  7300.00, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000012', 'Radia Perlman',     'SAVINGS',  31000.00, 'USD', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000013', 'Vint Cerf',         'CHECKING',  2480.25, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000014', 'Ken Thompson',      'CHECKING',  6100.00, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000015', 'Shafi Goldwasser',  'SAVINGS',  88000.00, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000016', 'Leslie Lamport',    'CHECKING',  410.00,  'EUR', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000017', 'John McCarthy',     'CHECKING',  1290.90, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000018', 'Frances Allen',     'SAVINGS',  15600.00, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000019', 'Niklaus Wirth',     'CHECKING',  3030.30, 'TRY', 'ACTIVE');
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR33000000000020', 'Bjarne Stroustrup', 'CHECKING',  720.00,  'TRY', 'ACTIVE');
-- Frozen target for the 409 AccountFrozenException path.
INSERT INTO accounts(account_no, customer, type, balance, currency, status) VALUES ('TR330000000000099', 'Suspended Holdings', 'CHECKING', 12000.00, 'TRY', 'FROZEN');

-- ── Cards ─────────────────────────────────────────────────────────────────
-- One or two cards per active account. One BLOCKED card so the card path can
-- fall through to "no active card".
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000001', '4417', 'VISA',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000002', '8830', 'MASTERCARD', 'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000003', '2201', 'VISA',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000004', '7745', 'AMEX',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000005', '1190', 'VISA',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000006', '6612', 'MASTERCARD', 'BLOCKED');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000007', '9034', 'VISA',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000008', '5521', 'MASTERCARD', 'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000009', '3380', 'VISA',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000010', '4471', 'AMEX',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000011', '7788', 'VISA',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000012', '1023', 'MASTERCARD', 'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000013', '6655', 'VISA',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000014', '2244', 'VISA',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000015', '9911', 'MASTERCARD', 'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000016', '3377', 'VISA',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000017', '8800', 'MASTERCARD', 'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000018', '5566', 'VISA',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000019', '1414', 'AMEX',       'ACTIVE');
INSERT INTO cards(account_no, last4, network, status) VALUES ('TR33000000000020', '7272', 'VISA',       'ACTIVE');

-- ── Payees ──────────────────────────────────────────────────────────────────
INSERT INTO payees(owner_account, name, target_account, category) VALUES ('TR33000000000001', 'CITY-POWER',    'TR33000000000007', 'UTILITY');
INSERT INTO payees(owner_account, name, target_account, category) VALUES ('TR33000000000001', 'ACME-TELECOM',  'TR33000000000008', 'TELECOM');
INSERT INTO payees(owner_account, name, target_account, category) VALUES ('TR33000000000002', 'LANDLORD-LLC',  'TR33000000000011', 'RENT');
INSERT INTO payees(owner_account, name, target_account, category) VALUES ('TR33000000000005', 'WATERWORKS',    'TR33000000000012', 'UTILITY');
INSERT INTO payees(owner_account, name, target_account, category) VALUES ('TR33000000000007', 'ACME-TELECOM',  'TR33000000000008', 'TELECOM');
INSERT INTO payees(owner_account, name, target_account, category) VALUES ('TR33000000000011', 'CITY-POWER',    'TR33000000000007', 'UTILITY');

-- ── Seed ledger history ──────────────────────────────────────────────────────
-- A few POSTED entries so a freshly-loaded statement isn't empty.
INSERT INTO transactions(reference, from_account, to_account, amount, currency, type, status, reason) VALUES ('TRF-SEED0001', 'TR33000000000002', 'TR33000000000001', 250.00, 'TRY', 'TRANSFER',     'POSTED', 'OK');
INSERT INTO transactions(reference, from_account, to_account, amount, currency, type, status, reason) VALUES ('CRD-SEED0002', 'TR33000000000001', 'STARBUCKS',        14.50,  'TRY', 'CARD_PAYMENT', 'POSTED', 'OK');
INSERT INTO transactions(reference, from_account, to_account, amount, currency, type, status, reason) VALUES ('BIL-SEED0003', 'TR33000000000005', 'WATERWORKS',       89.90,  'EUR', 'BILL_PAYMENT', 'POSTED', 'OK');
INSERT INTO transactions(reference, from_account, to_account, amount, currency, type, status, reason) VALUES ('TRF-SEED0004', 'TR33000000000009', 'TR33000000000003', 500.00, 'TRY', 'TRANSFER',     'DECLINED', 'INSUFFICIENT_FUNDS');
