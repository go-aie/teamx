# Todo 同文件并行实验

只需 Python 3 标准库。在本目录运行：

```sh
python3 overlap_todo.py --file tasks.json add 买牛奶
python3 overlap_todo.py --file tasks.json list
python3 overlap_todo.py --file tasks.json done 1
python3 -m unittest -v test_overlap_todo
```

数据格式为 `{"version":1,"next_id":1,"tasks":[]}`，任务包含 `id`、`title`、`done`。列表按 ID 升序，读取不修改文件；完成状态与下一个 ID 持久保存。

后续三个开发者会修改同一份 `overlap_todo.py`、`test_overlap_todo.py` 和本说明，分别增加优先级、状态过滤／统计、JSON 输出，再核对集成后的组合行为。
