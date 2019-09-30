#!/bin/bash

set -e

# How many times to run each benchmark
COUNT=20
FILES=("newO.txt" "oldO.txt" "shardO.txt")

# Remove all files from previous runs
rm -rf ./newO.txt ./oldO.txt /shardO.txt ./new.txt ./old.txt ./shard.txt ./old_new.txt ./old_shard.txt ./shard_new.txt ./*.txt-e ./*.out ./go-cache.test

# Run all benchmarks
go test -run=^$ github.com/Gobd/go-cache -cpuprofile cpuNew.out -count="$COUNT" -bench "BenchmarkCacheGetManyConcurrent.+" | tee ./newO.txt
go test -run=^$ github.com/patrickmn/go-cache -cpuprofile cpuOld.out -count="$COUNT" -bench "BenchmarkCacheGetManyConcurrent.+" | tee ./oldO.txt
go test -run=^$ github.com/patrickmn/go-cache -cpuprofile cpuShard.out -count="$COUNT" -bench "BenchmarkShardedCacheGetManyConcurrent.+" | tee ./shardO.txt

# Clean up file so it works with benchstat
sed -i -e 's/Sharded//g' ./shardO.txt

for i in "${FILES[@]}"
do
    # Clean up files so they work with benchstat
    sed -i -e 1,3d "$i"
    head -n $((COUNT*2)) "$i" > "${i/O/}"
    rm "$i"
    echo 
done

# Do benchstat comparisons
echo "patrickmn/go-cache vs Gobd/go-cache"
benchstat old.txt new.txt | tee old_new.txt

echo -e "\npatrickmn/go-cache vs patrickmn/go-cache (sharded)"
benchstat old.txt shard.txt | tee old_shard.txt

echo -e "\npatrickmn/go-cache (sharded) vs Gobd/go-cache"
benchstat shard.txt new.txt | tee shard_new.txt

# Remove everything but final comparisons
rm -rf /new.txt ./old.txt ./shard.txt ./*.txt-e
