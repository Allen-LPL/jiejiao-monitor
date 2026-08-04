# 打卡检查服务 API 文档

## 基本信息

- **服务端口**: 8080
- **基础路径**: `http://<host>:8080`

---

## 接口列表

### 查询打卡状态

主动查询当日各时段的打卡状态。

**请求**

```
GET /api/check
```

**请求参数**

无

**响应**

| 字段 | 类型 | 说明 |
|------|------|------|
| code | int | 状态码，200 表示成功 |
| message | string | 响应消息 |
| timestamp | string | 响应时间 (格式: YYYY-MM-DD HH:mm:ss) |
| data | object | 打卡数据 |
| data.slots | array | 各时段打卡状态列表 |

**slots 数组元素**

| 字段 | 类型 | 说明 |
|------|------|------|
| name | string | 时段名称 |
| start_time | string | 开始时间 (HH:mm) |
| end_time | string | 结束时间 (HH:mm) |
| checked | bool | 是否已打卡 |
| report_time | string | 打卡时间，未打卡时不返回 |

**响应示例**

成功响应：

```json
{
  "code": 200,
  "message": "success",
  "timestamp": "2026-01-26 15:30:00",
  "data": {
    "slots": [
      {
        "name": "第一次打卡(08:45-09:00)",
        "start_time": "08:45",
        "end_time": "09:00",
        "checked": true,
        "report_time": "08:50:23"
      },
      {
        "name": "第二次打卡(14:45-15:00)",
        "start_time": "14:45",
        "end_time": "15:00",
        "checked": false
      },
      {
        "name": "第三次打卡(20:45-21:00)",
        "start_time": "20:45",
        "end_time": "21:00",
        "checked": false
      }
    ]
  }
}
```

错误响应：

```json
{
  "code": 500,
  "message": "获取打卡状态失败",
  "timestamp": "2026-01-26 15:30:00"
}
```

**HTTP 状态码**

| 状态码 | 说明 |
|--------|------|
| 200 | 成功 |
| 405 | 方法不允许（仅支持 GET） |
| 500 | 服务器内部错误 |

---

## 调用示例

### cURL

```bash
curl -X GET http://localhost:8080/api/check
```

### JavaScript (Fetch)

```javascript
fetch('http://localhost:8080/api/check')
  .then(res => res.json())
  .then(data => console.log(data));
```

### Python (requests)

```python
import requests

response = requests.get('http://localhost:8080/api/check')
print(response.json())
```

---

## CORS 支持

接口已启用 CORS，支持跨域访问：

- `Access-Control-Allow-Origin: *`
- `Access-Control-Allow-Methods: GET, OPTIONS`
- `Access-Control-Allow-Headers: Content-Type`
