CREATE PROCEDURE acme.unterminated
AS
BEGIN
    SET NOCOUNT ON;
    IF 1 = 1
    BEGIN
        SELECT 1;
    END
