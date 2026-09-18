output "primary_endpoint" {
  description = "host:port for externalRedis.addr."
  value       = "${aws_elasticache_replication_group.this.primary_endpoint_address}:6379"
}

output "reader_endpoint" {
  value = aws_elasticache_replication_group.this.reader_endpoint_address
}
