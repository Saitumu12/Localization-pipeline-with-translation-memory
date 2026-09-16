-- Runs once when the Postgres volume is first created. The integration tests
-- use their own database so they can truncate freely without touching the
-- data you are working with.
CREATE DATABASE loc_test;
