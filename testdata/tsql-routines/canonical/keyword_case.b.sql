create procedure acme.touch_order @order_id int as begin if @order_id is null return; update acme.orders set touched_at = sysutcdatetime() where order_id = @order_id; end
